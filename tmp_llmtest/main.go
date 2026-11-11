package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func main() {
	body := map[string]any{
		"model":    "nemotron-worker",
		"messages": []map[string]string{{"role": "user", "content": "Return JSON"}},
		"stream":   false,
	}
	b, _ := json.Marshal(body)
	fmt.Println(string(b))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://localhost:11434/api/chat", "application/json", bytes.NewReader(b))
	fmt.Println("err", err)
	if err == nil {
		defer resp.Body.Close()
		var r map[string]any
		json.NewDecoder(resp.Body).Decode(&r)
		fmt.Printf("resp: %+v\n", r["message"])
	}
}
