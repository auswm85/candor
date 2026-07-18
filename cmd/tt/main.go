package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/store"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "tt",
	Short: "token-tracker — local-first LLM cost monitor",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
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

var spendCmd = &cobra.Command{
	Use:   "spend [today|month]",
	Short: "Show recorded LLM spend",
	Long: `Print recorded spend for a period.

Examples:
  tt spend today             Today's total spend
  tt spend month             This month's total spend
  tt spend month --by-model  This month's spend, broken down by model`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		period := "today"
		if len(args) > 0 {
			period = strings.ToLower(args[0])
		}

		now := time.Now()
		var since time.Time
		switch period {
		case "today":
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		case "month":
			since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		default:
			return fmt.Errorf("unknown period %q (use 'today' or 'month')", period)
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		st, err := store.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer func() { _ = st.Close() }()
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		byModel, _ := cmd.Flags().GetBool("by-model")
		if byModel {
			rows, err := st.CostByModelSince(since)
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			if len(rows) == 0 {
				fmt.Printf("No usage recorded since %s.\n", since.Format("2006-01-02"))
				return nil
			}
			var total float64
			for _, r := range rows {
				fmt.Printf("  %-32s $%8.2f\n", r.Provider+"/"+r.Model, r.CostUSD)
				total += r.CostUSD
			}
			fmt.Printf("  %-32s $%8.2f\n", "TOTAL", total)
			return nil
		}

		total, err := st.TotalCostSince(since)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		label := strings.ToUpper(period[:1]) + period[1:]
		fmt.Printf("%s spend: $%.2f\n", label, total)
		return nil
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending database migrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		st, err := store.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer func() { _ = st.Close() }()
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		fmt.Printf("Migrations applied. Database: %s\n", cfg.Database)
		return nil
	},
}

func init() {
	authCmd.Flags().Bool("list", false, "List configured providers")
	spendCmd.Flags().Bool("by-model", false, "Break spend down by model")
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(clearCmd)
	rootCmd.AddCommand(spendCmd)
	rootCmd.AddCommand(migrateCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
