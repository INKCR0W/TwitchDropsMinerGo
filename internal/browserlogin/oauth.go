package browserlogin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"twitchdropsminergo/internal/httpclient"
)

func loginURL() (string, string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", "", errors.New("生成授权校验码失败")
	}
	state := hex.EncodeToString(nonce)
	redirect := url.Values{"client_id": {httpclient.AndroidAppClient.ClientID}, "redirect_uri": {"https://www.twitch.tv/"}, "response_type": {"token"}, "state": {state}}
	query := url.Values{"client_id": {httpclient.AndroidAppClient.ClientID}, "redirect_params": {redirect.Encode()}}
	return "https://www.twitch.tv/login?" + query.Encode(), state, nil
}

func callbackToken(raw, state string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil
	}
	if u.Scheme != "https" || u.Host != "www.twitch.tv" || u.Path != "/" || u.User != nil {
		return "", nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("授权回调格式无效")
	}
	if query.Get("error") != "" {
		if len(query["state"]) != 1 || query.Get("state") != state {
			return "", errors.New("授权校验码不匹配，请重新登录")
		}
		return "", errors.New("本次 Twitch 授权未获批准")
	}
	values, err := url.ParseQuery(u.Fragment)
	if err != nil {
		return "", errors.New("授权回调格式无效")
	}
	if values.Get("access_token") == "" && values.Get("error") == "" {
		return "", nil
	}
	if len(values["state"]) != 1 || values.Get("state") != state {
		return "", errors.New("授权校验码不匹配，请重新登录")
	}
	if values.Get("error") != "" {
		return "", errors.New("本次 Twitch 授权未获批准")
	}
	token := values.Get("access_token")
	if len(values["access_token"]) != 1 || token == "" || strings.ContainsAny(token, " \r\n\t") || !strings.EqualFold(values.Get("token_type"), "bearer") {
		return "", errors.New("授权回调缺少有效令牌")
	}
	return token, nil
}
