// Package senderdomain manages business sending domains: ownership
// verification and SPF/DKIM/DMARC authentication (PRD F5), plus DKIM keys
// used by the delivery pipeline (PRD F6).
package senderdomain

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/metrics"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

type Domain struct {
	ID                string      `json:"id"`
	WorkspaceID       string      `json:"workspace_id"`
	Name              string      `json:"name"`
	VerificationToken string      `json:"-"`
	DKIMSelector      string      `json:"dkim_selector"`
	DKIMPublicKey     string      `json:"-"`
	Verified          bool        `json:"verified"`
	SPFOK             bool        `json:"spf_ok"`
	DKIMOK            bool        `json:"dkim_ok"`
	DMARCOK           bool        `json:"dmarc_ok"`
	LastCheckedAt     *time.Time  `json:"last_checked_at,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	Records           []DNSRecord `json:"dns_records"`
}

// CanSend reports whether the domain passes the minimum sending bar.
func (d Domain) CanSend() bool { return d.Verified && d.SPFOK && d.DKIMOK }

// DNSRecord is one record the customer must publish (shown in the setup wizard).
type DNSRecord struct {
	Purpose  string `json:"purpose"` // verification|spf|dkim|dmarc
	Type     string `json:"type"`
	Host     string `json:"host"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
}

// Resolver is the subset of *net.Resolver used for checks.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type Service struct {
	db         *pgxpool.Pool
	cipher     *security.Cipher
	resolver   Resolver
	spfInclude string
	selector   string
	// SkipDNS marks every record as passing. Development only: lets a local
	// stack send to Mailpit without publishing real DNS records.
	SkipDNS bool
}

func NewService(db *pgxpool.Pool, c *security.Cipher, r Resolver, spfInclude, selector string) *Service {
	return &Service{db: db, cipher: c, resolver: r, spfInclude: spfInclude, selector: selector}
}

const cols = `id, workspace_id, name, verification_token, dkim_selector, dkim_public_key,
	verified, spf_ok, dkim_ok, dmarc_ok, last_checked_at, created_at`

func scan(row pgx.Row) (Domain, error) {
	var d Domain
	err := row.Scan(&d.ID, &d.WorkspaceID, &d.Name, &d.VerificationToken, &d.DKIMSelector, &d.DKIMPublicKey,
		&d.Verified, &d.SPFOK, &d.DKIMOK, &d.DMARCOK, &d.LastCheckedAt, &d.CreatedAt)
	return d, err
}

func (s *Service) Add(ctx context.Context, wsID, name string) (Domain, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if !emailaddr.ValidDomain(name) {
		return Domain{}, apperr.Invalid("invalid_domain", "domain name is not valid")
	}
	if emailaddr.IsFreeProvider(name) || emailaddr.IsDisposable(name) {
		return Domain{}, apperr.Invalid("business_domain_required", "only domains you own can be used for sending")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Domain{}, apperr.Internal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	sealed, err := s.cipher.Encrypt(privPEM)
	if err != nil {
		return Domain{}, apperr.Internal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return Domain{}, apperr.Internal(err)
	}
	d, err := scan(s.db.QueryRow(ctx, `
		INSERT INTO sender_domains (workspace_id, name, verification_token, dkim_selector, dkim_private_key, dkim_public_key)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+cols,
		wsID, name, security.RandomHex(16), s.selector, sealed, base64.StdEncoding.EncodeToString(pubDER)))
	if database.IsUniqueViolation(err) {
		return Domain{}, apperr.Conflict("domain_exists", "domain already added to this workspace")
	}
	if err != nil {
		return Domain{}, apperr.Internal(err)
	}
	d.Records = ExpectedRecords(d, s.spfInclude)
	return d, nil
}

func (s *Service) List(ctx context.Context, wsID string) ([]Domain, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM sender_domains WHERE workspace_id=$1 ORDER BY name`, wsID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Domain, error) { return scan(r) })
	for i := range out {
		out[i].Records = ExpectedRecords(out[i], s.spfInclude)
	}
	return out, err
}

func (s *Service) Get(ctx context.Context, wsID, id string) (Domain, error) {
	d, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM sender_domains WHERE id=$1 AND workspace_id=$2`, id, wsID))
	if database.IsNoRows(err) {
		return d, apperr.NotFound("domain_not_found", "domain not found")
	}
	if err != nil {
		return d, apperr.Internal(err)
	}
	d.Records = ExpectedRecords(d, s.spfInclude)
	return d, nil
}

// Check re-runs DNS checks and persists the result.
func (s *Service) Check(ctx context.Context, wsID, id string) (Domain, error) {
	d, err := s.Get(ctx, wsID, id)
	if err != nil {
		return d, err
	}
	d.Records = CheckDNS(ctx, s.resolver, d, s.spfInclude)
	if s.SkipDNS {
		for i := range d.Records {
			d.Records[i].OK = true
		}
	}
	for _, r := range d.Records {
		switch r.Purpose {
		case "verification":
			// Ownership is sticky: customers often delete the TXT record after verifying.
			d.Verified = d.Verified || r.OK
		case "spf":
			d.SPFOK = r.OK
		case "dkim":
			d.DKIMOK = r.OK
		case "dmarc":
			d.DMARCOK = r.OK
		}
	}
	_, err = s.db.Exec(ctx, `
		UPDATE sender_domains SET verified=$2, spf_ok=$3, dkim_ok=$4, dmarc_ok=$5, last_checked_at=now() WHERE id=$1`,
		d.ID, d.Verified, d.SPFOK, d.DKIMOK, d.DMARCOK)
	if database.IsUniqueViolation(err) {
		return d, apperr.Conflict("domain_claimed", "this domain is already verified by another workspace")
	}
	if err != nil {
		return d, apperr.Internal(err)
	}
	return d, nil
}

// RecheckAll re-runs DNS checks for domains not checked in the last 6 hours,
// so a broken SPF/DKIM record blocks sending before it hurts reputation.
func (s *Service) RecheckAll(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT workspace_id, id, spf_ok AND dkim_ok FROM sender_domains
		WHERE verified AND (last_checked_at IS NULL OR last_checked_at < now() - interval '6 hours')
		ORDER BY last_checked_at NULLS FIRST LIMIT 200`)
	if err != nil {
		return err
	}
	type item struct {
		ws, id string
		wasOK  bool
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		return it, r.Scan(&it.ws, &it.id, &it.wasOK)
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		d, err := s.Check(ctx, it.ws, it.id)
		if err != nil {
			metrics.DomainChecks.WithLabelValues("error").Inc()
			continue
		}
		if d.CanSend() {
			metrics.DomainChecks.WithLabelValues("ok").Inc()
			continue
		}
		metrics.DomainChecks.WithLabelValues("failing").Inc()
		if it.wasOK {
			slog.Warn("sender domain lost authentication; sending is blocked until fixed",
				"domain", d.Name, "workspace_id", it.ws, "spf_ok", d.SPFOK, "dkim_ok", d.DKIMOK)
		}
	}
	return nil
}

// ExpectedRecords lists what the customer must publish.
func ExpectedRecords(d Domain, spfInclude string) []DNSRecord {
	return []DNSRecord{
		{Purpose: "verification", Type: "TXT", Host: "_balasemail." + d.Name, Value: "balasemail-verification=" + d.VerificationToken, Required: true, OK: d.Verified},
		{Purpose: "spf", Type: "TXT", Host: d.Name, Value: "v=spf1 include:" + spfInclude + " ~all", Required: true, OK: d.SPFOK},
		{Purpose: "dkim", Type: "TXT", Host: d.DKIMSelector + "._domainkey." + d.Name, Value: "v=DKIM1; k=rsa; p=" + d.DKIMPublicKey, Required: true, OK: d.DKIMOK},
		{Purpose: "dmarc", Type: "TXT", Host: "_dmarc." + d.Name, Value: "v=DMARC1; p=none; rua=mailto:dmarc@" + d.Name, Required: false, OK: d.DMARCOK},
	}
}

// CheckDNS evaluates each expected record against live DNS.
func CheckDNS(ctx context.Context, r Resolver, d Domain, spfInclude string) []DNSRecord {
	recs := ExpectedRecords(d, spfInclude)
	lookup := func(host string) []string {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		txt, _ := r.LookupTXT(ctx, host)
		return txt
	}
	for i := range recs {
		rec := &recs[i]
		txts := lookup(rec.Host)
		rec.OK = false
		for _, t := range txts {
			t = strings.TrimSpace(t)
			switch rec.Purpose {
			case "verification":
				rec.OK = rec.OK || t == rec.Value
			case "spf":
				rec.OK = rec.OK || (strings.HasPrefix(strings.ToLower(t), "v=spf1") && strings.Contains(t, "include:"+spfInclude))
			case "dkim":
				rec.OK = rec.OK || strings.Contains(strings.ReplaceAll(t, " ", ""), "p="+d.DKIMPublicKey)
			case "dmarc":
				rec.OK = rec.OK || strings.HasPrefix(strings.ToUpper(strings.ReplaceAll(t, " ", "")), "V=DMARC1")
			}
		}
	}
	return recs
}

// AuthorizeSender returns the domain for fromEmail if it belongs to the
// workspace and passes authentication. Every send path calls this (PRD F5).
func (s *Service) AuthorizeSender(ctx context.Context, wsID, fromEmail string) (Domain, error) {
	addr, err := emailaddr.Parse(fromEmail)
	if err != nil {
		return Domain{}, apperr.Invalid("invalid_from", "from address is not valid")
	}
	d, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM sender_domains WHERE workspace_id=$1 AND name=$2`, wsID, addr.Domain))
	if database.IsNoRows(err) {
		return d, apperr.Forbidden("domain_not_registered", "add and verify "+addr.Domain+" before sending from it")
	}
	if err != nil {
		return d, apperr.Internal(err)
	}
	switch {
	case !d.Verified:
		return d, apperr.Forbidden("domain_not_verified", "domain ownership is not verified")
	case !d.SPFOK:
		return d, apperr.Forbidden("spf_missing", "SPF record is missing or incorrect")
	case !d.DKIMOK:
		return d, apperr.Forbidden("dkim_missing", "DKIM record is missing or incorrect")
	}
	return d, nil
}

// SigningKey returns the DKIM selector and private key for a domain.
func (s *Service) SigningKey(ctx context.Context, domain string) (string, crypto.Signer, error) {
	var selector string
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT dkim_selector, dkim_private_key FROM sender_domains WHERE name=$1 AND verified`, domain).
		Scan(&selector, &sealed)
	if err != nil {
		return "", nil, err
	}
	plain, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return "", nil, err
	}
	block, _ := pem.Decode(plain)
	if block == nil {
		return "", nil, apperr.Internal(nil)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	return selector, key, err
}

// Lookup resolves a verified domain name to its owning workspace (inbound MX routing).
func (s *Service) Lookup(ctx context.Context, domain string) (wsID, domainID string, ok bool) {
	err := s.db.QueryRow(ctx, `SELECT workspace_id, id FROM sender_domains WHERE name=$1 AND verified`, strings.ToLower(domain)).
		Scan(&wsID, &domainID)
	return wsID, domainID, err == nil
}
