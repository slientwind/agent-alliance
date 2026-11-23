package resources

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"
)

const ImageTimeout = 10 * time.Minute
const providerTimeout = 90 * time.Second
const toolTimeout = 45 * time.Second

func (m Model) SubmissionWindows() (int, int) {
	if m.External {
		return m.Policy.ExecutionSeconds, m.Policy.WindowSeconds
	}
	execution := providerTimeout
	if ProducesPNG(m.OutputOperation) {
		execution = ImageTimeout
	}
	if m.Tool != nil {
		execution = toolTimeout
	}
	return int(execution / time.Second), defaultRemotePolicy().WindowSeconds
}

var providerClient = sync.OnceValues(func() (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if filename := os.Getenv("PROVIDER_CA_FILE"); filename != "" {
		raw, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid enterprise trust root")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: providerTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
})
