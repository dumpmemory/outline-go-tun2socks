// Copyright 2021 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shadowsocks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

// ProxyConfig represents a Shadowsocks proxy configuration.
type ProxyConfig struct {
	Host       string `json:"server"`
	Port       int    `json:"server_port"`
	Password   string `json:"password"`
	Cipher     string `json:"method"`
	Name       string `json:"remarks,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	PluginOpts string `json:"plugin_opts,omitempty"`
}

// FetchConfigRequest encapsulates a request to an online config server.
type FetchConfigRequest struct {
	// URL is the HTTPs endpoint of an online config server.
	URL string
	// Method is the HTTP method to use in the request.
	Method string
	// TrustedCertFingerprint is the sha256 hash of the online config server's
	// TLS certificate.
	TrustedCertFingerprint []byte
}

// FetchConfigResponse encapsulates a response and metadata from an online config server.
type FetchConfigResponse struct {
	// Proxies is a list of Shadowsocks proxy configurations
	Proxies []ProxyConfig
	// HTTPStatusCode is the HTTP status code of the response.
	HTTPStatusCode int
	// RedirectURL is the Location header of a HTTP redirect response.
	RedirectURL string
}

// sip008Response represents a JSON response from an online config server.
type sip008Response struct {
	Proxies []ProxyConfig `json:"servers"`
	Version int
}

// FetchConfig retrieves Shadowsocks proxy configurations per SIP008:
// https://github.com/shadowsocks/shadowsocks-org/wiki/SIP008-Online-Configuration-Delivery
//
// Pins the trusted certificate when req.TrustedCertFingerprint is non-empty.
// Sets the response's RedirectURL when the status code is a redirect.
// Returns an error if req.URL is a non-HTTPS URL, if there is a connection
// error to the server, or if parsing the configuration fails.
func FetchConfig(req FetchConfigRequest) (*FetchConfigResponse, error) {
	httpreq, err := http.NewRequest(req.Method, req.URL, nil)
	if err != nil {
		return nil, err
	}
	if httpreq.URL.Scheme != "https" {
		return nil, errors.New("URL protocol must be HTTPs")
	}

	client := &http.Client{
		// Do not follow redirects automatically, relay to the caller.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	if req.TrustedCertFingerprint != nil {
		client.Transport = &http.Transport{
			// Perform custom server certificate verification by pinning the
			// trusted certificate fingerprint.
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify:    true,
				VerifyPeerCertificate: makePinnedCertVerifier(req.TrustedCertFingerprint),
			},
		}
	}

	httpres, err := client.Do(httpreq)
	if err != nil {
		return nil, err
	}

	var res FetchConfigResponse
	res.HTTPStatusCode = httpres.StatusCode
	if res.HTTPStatusCode >= 300 && res.HTTPStatusCode < 400 {
		// Redirect
		res.RedirectURL = httpres.Header.Get("Location")
		return &res, nil
	} else if res.HTTPStatusCode > 400 {
		// HTTP error
		return &res, nil
	}

	// 2xx status code
	defer httpres.Body.Close()
	var sip008res sip008Response
	err = json.NewDecoder(httpres.Body).Decode(&sip008res)
	res.Proxies = sip008res.Proxies
	return &res, err
}

type tlsDialer func(ctx context.Context, network, addr string) (net.Conn, error)

type certVerifier func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error

// Verifies whether the pinned  certificate SHA256 fingerprint,
// trustedCertFingerprint, matches a fingerprint in the certificate chain,
// regardless of the system's TLS certificate validation errors.
func makePinnedCertVerifier(trustedCertFingerprint []byte) certVerifier {
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		for _, cert := range rawCerts {
			fingerprint := computeCertificateFingerprint(cert)
			if bytes.Equal(fingerprint, trustedCertFingerprint) {
				return nil
			}
		}
		return errors.New("Failed to validate TLS certificate")
	}
}

// Computes the sha256 digest of the whole DER-encoded certificate and
// returns it as a base64-encoded string.
func computeCertificateFingerprint(derCert []byte) []byte {
	hash := sha256.Sum256(derCert)
	return hash[:]
}