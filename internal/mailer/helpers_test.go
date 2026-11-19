package mailer

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"mime"
)

type headerDecoder struct{ mime.WordDecoder }

func (d *headerDecoder) decode(s string) (string, error) { return d.DecodeHeader(s) }

func dkimRecord(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
}
