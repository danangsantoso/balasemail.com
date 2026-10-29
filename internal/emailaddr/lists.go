package emailaddr

// These lists are intentionally small seeds. In production they are refreshed
// from maintained sources (e.g. disposable-email-domains) by a scheduled job.

var freeProviders = set(
	"gmail.com", "googlemail.com", "yahoo.com", "yahoo.co.id", "ymail.com", "rocketmail.com",
	"outlook.com", "outlook.co.id", "hotmail.com", "hotmail.co.id", "live.com", "msn.com",
	"icloud.com", "me.com", "mac.com", "aol.com", "gmx.com", "gmx.net", "mail.com",
	"yandex.com", "yandex.ru", "proton.me", "protonmail.com", "zoho.com", "zohomail.com",
	"tutanota.com", "qq.com", "163.com", "126.com", "rediffmail.com", "mail.ru",
	"plasa.com", "telkom.net",
)

var disposable = set(
	"mailinator.com", "guerrillamail.com", "guerrillamail.net", "10minutemail.com",
	"temp-mail.org", "tempmail.com", "throwawaymail.com", "yopmail.com", "trashmail.com",
	"getnada.com", "dispostable.com", "sharklasers.com", "maildrop.cc", "fakeinbox.com",
	"mintemail.com", "mohmal.com", "emailondeck.com", "tempail.com", "moakt.com",
)

var roleLocals = set(
	"admin", "administrator", "info", "sales", "support", "help", "contact", "kontak",
	"marketing", "billing", "finance", "hr", "hrd", "jobs", "career", "careers", "karir",
	"office", "hello", "halo", "team", "webmaster", "postmaster", "abuse", "noc",
	"security", "legal", "privacy", "cs", "customerservice", "service", "enquiries", "inquiry",
)

func set(vs ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vs))
	for _, v := range vs {
		m[v] = struct{}{}
	}
	return m
}
