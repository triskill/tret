package job_workers

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type UserTaskWorker struct{}

func NewUserTaskWorker() *UserTaskWorker {
	return &UserTaskWorker{}
}

func (w *UserTaskWorker) Handle() (string, error) {
	fmt.Println("[UserTaskWorker] Waiting for human input (Type 'submit' to finish task)...")
	scanner := bufio.NewScanner(os.Stdin)
	var history []string

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := scanner.Text()
		if input == "" {
			continue
		}
		if strings.ToLower(input) == "submit" {
			return strings.Join(history, "\n"), nil
		}
		if strings.ToLower(input) == "exit" {
			return "", fmt.Errorf("user aborted task")
		}

		fmt.Printf("[User] %s\n", input)
		history = append(history, input)
		fmt.Println("[Agent] (Listening...)")
	}
	return strings.Join(history, "\n"), nil
}
