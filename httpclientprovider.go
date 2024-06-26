package lksdk

import "net/http"

type HttpClientProvider struct {
	NewHttpClient func(name string) *http.Client
}

var DefaultHttpClientProvider = &HttpClientProvider{}

func (p *HttpClientProvider) newHttpClient(name string) *http.Client {
	fn := p.NewHttpClient
	if fn == nil {
		fn = defaultNewHttpClient
	}
	return fn(name)
}

func defaultNewHttpClient(name string) *http.Client {
	return &http.Client{}
}
