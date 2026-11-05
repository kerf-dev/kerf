package datasource

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

type seen struct {
	header http.Header
	body   string
	path   string
	query  url.Values
}

// promServer answers the query and label APIs and records the last request.
func promServer(t *testing.T, last *seen, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*last = seen{header: r.Header.Clone(), body: string(b), path: r.URL.Path, query: r.URL.Query()}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("no org id"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/label/") {
			_, _ = w.Write([]byte(`{"status":"success","data":["c","a","b"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
}

func staticCreds(_ context.Context, _ hubapi.DatasourceSigV4) (aws.CredentialsProvider, error) {
	return credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""), nil
}

func TestHTTPClientAuth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		check func(t *testing.T, s seen)
	}{
		{"none", Config{AuthType: hubapi.AuthNone}, func(t *testing.T, s seen) {
			if s.header.Get("Authorization") != "" {
				t.Error("unexpected Authorization")
			}
		}},
		{"bearer", Config{AuthType: hubapi.AuthBearer, Secrets: Secrets{Token: "tok"}}, func(t *testing.T, s seen) {
			if got := s.header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("Authorization = %q", got)
			}
		}},
		{"basic", Config{AuthType: hubapi.AuthBasic, Auth: AuthConfig{Username: "u"}, Secrets: Secrets{Password: "p"}}, func(t *testing.T, s seen) {
			if got := s.header.Get("Authorization"); got != "Basic dTpw" {
				t.Errorf("Authorization = %q", got)
			}
		}},
		{"headers", Config{AuthType: hubapi.AuthNone, Headers: map[string]string{"X-Scope-OrgID": "tenant-1"}}, func(t *testing.T, s seen) {
			if got := s.header.Get("X-Scope-OrgID"); got != "tenant-1" {
				t.Errorf("X-Scope-OrgID = %q", got)
			}
		}},
		{"sigv4", Config{AuthType: hubapi.AuthSigV4, Auth: AuthConfig{SigV4: &hubapi.DatasourceSigV4{Region: "eu-west-1"}}, Headers: map[string]string{"X-Extra": "1"}}, func(t *testing.T, s seen) {
			a := s.header.Get("Authorization")
			if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") || !strings.Contains(a, "/eu-west-1/aps/aws4_request") {
				t.Errorf("Authorization = %q", a)
			}
			if !strings.Contains(a, "x-extra") {
				t.Errorf("custom header not signed: %q", a)
			}
			if s.header.Get("X-Amz-Date") == "" {
				t.Error("no X-Amz-Date")
			}
			if s.body != "query=up" {
				t.Errorf("body = %q", s.body)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var last seen
			srv := promServer(t, &last, http.StatusOK)
			defer srv.Close()
			hc, err := tc.cfg.HTTPClient(context.Background(), staticCreds)
			if err != nil {
				t.Fatal(err)
			}
			res, err := hc.Post(srv.URL+"/api/v1/query", "application/x-www-form-urlencoded", strings.NewReader("query=up"))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			tc.check(t, last)
		})
	}
}

func TestHTTPClientErrors(t *testing.T) {
	if _, err := (Config{AuthType: "kerberos"}).HTTPClient(context.Background(), nil); err == nil {
		t.Error("unknown auth type accepted")
	}
	if _, err := (Config{AuthType: hubapi.AuthSigV4}).HTTPClient(context.Background(), staticCreds); err == nil {
		t.Error("sigv4 without region accepted")
	}
	if _, err := (Config{AuthType: hubapi.AuthNone, Auth: AuthConfig{CACert: "junk"}}).HTTPClient(context.Background(), nil); err == nil {
		t.Error("bad CA accepted")
	}
	if _, err := (Config{AuthType: hubapi.AuthMTLS}).HTTPClient(context.Background(), nil); err == nil {
		t.Error("mtls without certificate accepted")
	}
	if s := (Config{Name: "x", Secrets: Secrets{Token: "hidden"}}).String(); strings.Contains(s, "hidden") {
		t.Error("String leaks the token")
	}
}

// pemPair returns a self-signed certificate and key in PEM.
func pemPair(t *testing.T, cn string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (certPEM, keyPEM string, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: isCA, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ = x509.ParseCertificate(der)
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})), cert, key
}

func TestHTTPClientMTLS(t *testing.T) {
	_, _, caCert, caKey := pemPair(t, "client-ca", true, nil, nil)
	clientPEM, clientKey, _, _ := pemPair(t, "ro-hub", false, caCert, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "ro-hub" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	serverCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	cfg := Config{AuthType: hubapi.AuthMTLS, Auth: AuthConfig{CACert: serverCA, ClientCert: clientPEM}, Secrets: Secrets{ClientKey: clientKey}}
	hc, err := cfg.HTTPClient(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := NewAPI(srv.URL, hc).Probe(context.Background()); !r.OK {
		t.Fatalf("mtls probe: %+v", r)
	}
	// without the server CA the certificate is not trusted
	cfg.Auth.CACert = ""
	hc, _ = cfg.HTTPClient(context.Background(), nil)
	r := NewAPI(srv.URL, hc).Probe(context.Background())
	if r.OK || !strings.Contains(r.Hint, "caCert") {
		t.Errorf("untrusted server: %+v", r)
	}
}

func TestProbe(t *testing.T) {
	var last seen
	ok := promServer(t, &last, http.StatusOK)
	defer ok.Close()
	if r := NewAPI(ok.URL+"/", http.DefaultClient).Probe(context.Background()); !r.OK || r.HTTPStatus != 200 || last.query.Get("query") != "vector(1)" {
		t.Errorf("ok probe: %+v %v", r, last.query)
	}
	unauth := promServer(t, &last, http.StatusUnauthorized)
	defer unauth.Close()
	if r := NewAPI(unauth.URL, http.DefaultClient).Probe(context.Background()); r.OK || r.HTTPStatus != 401 || !strings.Contains(r.Hint, "X-Scope-OrgID") {
		t.Errorf("401 probe: %+v", r)
	}
	nf := promServer(t, &last, http.StatusNotFound)
	defer nf.Close()
	if r := NewAPI(nf.URL, http.DefaultClient).Probe(context.Background()); r.OK || !strings.Contains(r.Hint, "query base") {
		t.Errorf("404 probe: %+v", r)
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>grafana</html>")) }))
	defer html.Close()
	if r := NewAPI(html.URL, http.DefaultClient).Probe(context.Background()); r.OK || !strings.Contains(r.Error, "not a Prometheus") {
		t.Errorf("html probe: %+v", r)
	}
	five := promServer(t, &last, http.StatusBadGateway)
	defer five.Close()
	if r := NewAPI(five.URL, http.DefaultClient).Probe(context.Background()); r.OK || r.HTTPStatus != 502 {
		t.Errorf("502 probe: %+v", r)
	}
	if r := NewAPI("http://127.0.0.1:1", http.DefaultClient).Probe(context.Background()); r.OK || !strings.Contains(r.Hint, "connect") {
		t.Errorf("refused probe: %+v", r)
	}
	if r := NewAPI("http://no-such-host.invalid", http.DefaultClient).Probe(context.Background()); r.OK || !strings.Contains(r.Hint, "resolve") {
		t.Errorf("dns probe: %+v", r)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	if r := NewAPI(slow.URL, &http.Client{Timeout: 50 * time.Millisecond}).Probe(context.Background()); r.OK || !strings.Contains(r.Hint, "timeout") {
		t.Errorf("timeout probe: %+v", r)
	}
}

func TestLabelValues(t *testing.T) {
	var last seen
	srv := promServer(t, &last, http.StatusOK)
	defer srv.Close()
	api := NewAPI(srv.URL, http.DefaultClient)
	got, err := api.LabelValues(context.Background(), "k8s_cluster", []string{"up", `kube_pod_owner{env="x"}`}, time.Unix(100, 0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("values = %v", got)
	}
	if last.path != "/api/v1/label/k8s_cluster/values" || len(last.query["match[]"]) != 2 || last.query.Get("start") != "100" {
		t.Errorf("request %s %v", last.path, last.query)
	}
	bad := promServer(t, &last, http.StatusUnauthorized)
	defer bad.Close()
	if _, err := NewAPI(bad.URL, http.DefaultClient).LabelValues(context.Background(), "x", nil, time.Time{}, 0); err == nil {
		t.Error("401 accepted")
	}
}
