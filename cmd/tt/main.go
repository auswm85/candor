package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "tt",
	Short: "token-tracker — local-first LLM cost monitor",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var authCmd = &cobra.Command{
	Use:   "auth [provider]",
	Short: "Configure API keys for LLM providers",
	Long: `Set or view API keys for supported LLM providers.
Keys are stored in your OS keychain.

Providers: openai, anthropic, openrouter

Examples:
  tt auth              Interactive setup for all providers
  tt auth openai       Set OpenAI API key
  tt auth --list       Show which providers are configured`,
	RunE: func(cmd *cobra.Command, args []string) error {
		list, _ := cmd.Flags().GetBool("list")
		if list {
			configured := auth.ListConfiguredProviders()
			if len(configured) == 0 {
				fmt.Println("No providers configured.")
				return nil
			}
			fmt.Println("Configured providers:")
			for _, p := range configured {
				fmt.Printf("  ✓ %s\n", p)
			}
			return nil
		}

		providers := []string{"openai", "anthropic", "openrouter"}
		if len(args) > 0 {
			providers = args
		}

		reader := bufio.NewReader(os.Stdin)
		for _, p := range providers {
			if auth.HasProviderKey(p) {
				fmt.Printf("%s: already configured. Overwrite? (y/N): ", p)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Printf("  Skipping %s.\n", p)
					continue
				}
			}
			fmt.Printf("Enter %s API key: ", p)
			key, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				fmt.Printf("  Skipping %s (empty key).\n", p)
				continue
			}
			if err := auth.SetProviderKey(p, key); err != nil {
				return fmt.Errorf("store %s key: %w", p, err)
			}
			fmt.Printf("  ✓ %s configured.\n", p)
		}
		return nil
	},
}

var clearCmd = &cobra.Command{
	Use:   "clear [provider]",
	Short: "Clear stored API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			for _, p := range args {
				if err := auth.ClearProviderKey(p); err != nil {
					return fmt.Errorf("clear %s: %w", p, err)
				}
				fmt.Printf("  Cleared %s.\n", p)
			}
			return nil
		}
		for _, p := range []string{"openai", "anthropic", "openrouter"} {
			if auth.HasProviderKey(p) {
				if err := auth.ClearProviderKey(p); err != nil {
					return fmt.Errorf("clear %s: %w", p, err)
				}
				fmt.Printf("  Cleared %s.\n", p)
			}
		}
		return nil
	},
}

func init() {
	authCmd.Flags().Bool("list", false, "List configured providers")
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(clearCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
