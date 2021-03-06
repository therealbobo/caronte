package parsers

import (
	"bufio"
	"bytes"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"moul.io/http2curl"
	"net/http"
	"strings"
)

type HttpRequestMetadata struct {
	BasicMetadata
	Method        string                         `json:"method"`
	URL           string                         `json:"url"`
	Protocol      string                         `json:"protocol"`
	Host          string                         `json:"host"`
	Headers       map[string]string              `json:"headers"`
	Cookies       map[string]string              `json:"cookies" binding:"omitempty"`
	ContentLength int64                          `json:"content_length"`
	FormData      map[string]string              `json:"form_data" binding:"omitempty"`
	Body          string                         `json:"body" binding:"omitempty"`
	Trailer       map[string]string              `json:"trailer" binding:"omitempty"`
	Reproducers   HttpRequestMetadataReproducers `json:"reproducers"`
}

type HttpRequestMetadataReproducers struct {
	CurlCommand  string `json:"curl_command"`
	RequestsCode string `json:"requests_code"`
	FetchRequest string `json:"fetch_request"`
}

type HttpRequestParser struct {
}

func (p HttpRequestParser) TryParse(content []byte) Metadata {
	reader := bufio.NewReader(bytes.NewReader(content))
	request, err := http.ReadRequest(reader)
	if err != nil {
		return nil
	}
	var body string
	if buffer, err := ioutil.ReadAll(request.Body); err == nil {
		body = string(buffer)
	} else {
		log.WithError(err).Error("failed to read body in http_request_parser")
		return nil
	}
	_ = request.Body.Close()
	_ = request.ParseForm()

	return HttpRequestMetadata{
		BasicMetadata: BasicMetadata{"http-request"},
		Method:        request.Method,
		URL:           request.URL.String(),
		Protocol:      request.Proto,
		Host:          request.Host,
		Headers:       JoinArrayMap(request.Header),
		Cookies:       CookiesMap(request.Cookies()),
		ContentLength: request.ContentLength,
		FormData:      JoinArrayMap(request.Form),
		Body:          body,
		Trailer:       JoinArrayMap(request.Trailer),
		Reproducers: HttpRequestMetadataReproducers{
			CurlCommand:  curlCommand(content),
			RequestsCode: requestsCode(request),
			FetchRequest: fetchRequest(request, body),
		},
	}
}

func curlCommand(content []byte) string {
	// a new reader is required because all the body is read before and GetBody() doesn't works
	reader := bufio.NewReader(bytes.NewReader(content))
	request, _ := http.ReadRequest(reader)
	if command, err := http2curl.GetCurlCommand(request); err == nil {
		return command.String()
	} else {
		return err.Error()
	}
}

func requestsCode(request *http.Request) string {
	var b strings.Builder
	var params string
	if request.Form != nil {
		params = toJson(JoinArrayMap(request.PostForm))
	}
	headers := toJson(JoinArrayMap(request.Header))
	cookies := toJson(CookiesMap(request.Cookies()))

	b.WriteString("import requests\n\nresponse = requests." + strings.ToLower(request.Method) + "(")
	b.WriteString("\"" + request.URL.String() + "\"")
	if params != "" {
		b.WriteString(", data = " + params)
	}
	if headers != "" {
		b.WriteString(", headers = " + headers)
	}
	if cookies != "" {
		b.WriteString(", cookies = " + cookies)
	}
	b.WriteString(")\n")
	b.WriteString(`
# print(response.url)
# print(response.text)
# print(response.content)
# print(response.json())
# print(response.raw)
# print(response.status_code)
# print(response.cookies)
# print(response.history)
`)

	return b.String()
}

func fetchRequest(request *http.Request, body string) string {
	headers := JoinArrayMap(request.Header)
	data := make(map[string]interface{})
	data["headers"] = headers
	if referrer := request.Header.Get("referrer"); referrer != "" {
		data["Referrer"] = referrer
	}
	// TODO: referrerPolicy
	if body == "" {
		data["body"] = nil
	} else {
		data["body"] = body
	}
	data["method"] = request.Method
	// TODO: mode

	if jsonData := toJson(data); jsonData != "" {
		return "fetch(\"" + request.URL.String() + "\", " + jsonData + ");"
	} else {
		return "invalid-request"
	}
}

func toJson(obj interface{}) string {
	if buffer, err := json.MarshalIndent(obj, "", "\t"); err == nil {
		return string(buffer)
	} else {
		return ""
	}
}
