package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchParsesMultiNodeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("nodeid"); got != "ctcc|cmcc" {
			t.Fatalf("nodeid = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"msg":"获取成功",
			"data":{
				"ctcc":{"code":200,"info":[{"ip":"172.64.1.1","loss":"0.00%","ping":"100ms"}]},
				"cmcc":{"code":"200","info":[{"ip":"172.64.2.2","loss":"0.00%","ping":"120ms"}]}
			},
			"statu":"true",
			"code":"200"
		}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Endpoint:   server.URL,
		Source:     "api",
		Username:   "u",
		Key:        "k",
		HTTPClient: server.Client(),
	})
	got, err := client.Fetch(context.Background(), []string{"ctcc", "cmcc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["ctcc"]) != 1 || got["ctcc"][0].IP != "172.64.1.1" {
		t.Fatalf("unexpected ctcc result: %#v", got["ctcc"])
	}
	if len(got["cmcc"]) != 1 || got["cmcc"][0].APIPing != "120ms" {
		t.Fatalf("unexpected cmcc result: %#v", got["cmcc"])
	}
}

func TestFetchReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"msg":"bad key","statu":"false","code":-207}`))
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Source: "api", HTTPClient: server.Client()})
	if _, err := client.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchWebParsesRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><table>
			<tr><td>1</td><td>电信</td><td>172.64.82.114</td><td>0.00%</td><td>136.85ms</td><td>6.92mb/s</td><td>55.36mb</td></tr>
			<tr><td>2</td><td>联通</td><td>104.26.4.90</td><td>0.00%</td><td>68.57ms</td><td>10.27mb/s</td><td>82.16mb</td></tr>
			<tr><td>3</td><td>移动</td><td>104.18.81.19</td><td>0.00%</td><td>104.47ms</td><td>12.98mb/s</td><td>103.84mb</td></tr>
		</table></body></html>`))
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Source: "web", HTTPClient: server.Client()})
	got, err := client.Fetch(context.Background(), []string{"ctcc", "cmcc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["ctcc"]) != 1 || got["ctcc"][0].IP != "172.64.82.114" {
		t.Fatalf("unexpected ctcc result: %#v", got["ctcc"])
	}
	if len(got["cmcc"]) != 1 || got["cmcc"][0].APIPing != "104.47ms" {
		t.Fatalf("unexpected cmcc result: %#v", got["cmcc"])
	}
	if _, ok := got["cucc"]; ok {
		t.Fatalf("unexpected cucc results after filtering: %#v", got["cucc"])
	}
}
