package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type sendNotificationRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

func main() {
	endpoint := flag.String("endpoint", "https://pushnotification.newsorbit.tech/sendNotification", "sendNotification endpoint URL")
	title := flag.String("title", "", "notification title")
	body := flag.String("body", "", "notification body")
	urlValue := flag.String("url", "/", "click URL path or absolute URL")
	adminToken := flag.String("admin-token", strings.TrimSpace(os.Getenv("ADMIN_TOKEN")), "admin token (or set ADMIN_TOKEN env)")
	flag.Parse()

	if strings.TrimSpace(*title) == "" {
		exitf("--title is required")
	}
	if strings.TrimSpace(*body) == "" {
		exitf("--body is required")
	}
	if strings.TrimSpace(*adminToken) == "" {
		exitf("--admin-token is required (or set ADMIN_TOKEN env)")
	}
	if strings.TrimSpace(*urlValue) == "" {
		*urlValue = "/"
	}

	payload, err := json.Marshal(sendNotificationRequest{
		Title: *title,
		Body:  *body,
		URL:   *urlValue,
	})
	if err != nil {
		exitf("encode request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, *endpoint, bytes.NewReader(payload))
	if err != nil {
		exitf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Admin-Token", *adminToken)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		exitf("send request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		exitf("read response: %v", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		exitf("request failed (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	fmt.Println(strings.TrimSpace(string(respBody)))
}

func exitf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
