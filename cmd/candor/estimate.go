package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/pricing"
	"github.com/spf13/cobra"
)

var estimateCmd = &cobra.Command{
	Use:   "estimate --model <name> [--provider name] --prompt <text>",
	Short: "Estimate token count and cost before sending to an LLM",
	Long: `Estimate the cost of a prompt for a given provider + model using a rough
character-based token count (~4 chars per token for English).

  candor estimate --model gpt-4o --prompt "explain Go interfaces"
  candor estimate --model claude-sonnet-4-5 --provider anthropic --prompt "$(cat myfile.go)"
  candor estimate --model gpt-4o --prompt-file myfile.go

Prices are sourced from the dynamic OpenRouter catalog (cached for 24h), falling
back to bundled defaults when offline. The token estimate is approximate — actual
token counts depend on the model's specific tokenizer.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, _ := cmd.Flags().GetString("provider")
		model, _ := cmd.Flags().GetString("model")
		prompt, _ := cmd.Flags().GetString("prompt")
		promptFile, _ := cmd.Flags().GetString("prompt-file")

		if model == "" {
			return fmt.Errorf("--model is required")
		}
		if prompt == "" && promptFile == "" {
			return fmt.Errorf("--prompt or --prompt-file is required")
		}
		if prompt != "" && promptFile != "" {
			return fmt.Errorf("use --prompt or --prompt-file, not both")
		}
		if promptFile != "" {
			b, err := os.ReadFile(promptFile)
			if err != nil {
				return fmt.Errorf("read prompt file: %w", err)
			}
			prompt = string(b)
		}
		if prompt == "" {
			return fmt.Errorf("prompt is empty")
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		cacheDir := filepath.Dir(cfg.Database)
		prices := pricing.Load(cacheDir, cfg.Pricing.Source)
		engine := cost.New(prices)

		mp, hasPrice := engine.PriceFor(provider, model)
		if !hasPrice {
			return fmt.Errorf("no pricing found for %s/%s", provider, model)
		}

		chars := utf8.RuneCountInString(prompt)
		estimatedTokens := int64(chars / 4)
		if estimatedTokens < 1 {
			estimatedTokens = 1
		}

		costUSD := engine.Compute(provider, model, estimatedTokens, 0, 0, 0)

		fmt.Printf("Provider:          %s\n", provider)
		fmt.Printf("Model:             %s\n", model)
		fmt.Printf("Prompt chars:      %d\n", chars)
		fmt.Printf("Estimated tokens:  ~%d  (chars ÷ 4)\n", estimatedTokens)
		fmt.Printf("Input rate:         $%.2f / 1M tokens\n", mp.InputPer1M)
		fmt.Printf("Estimated cost:    $%.6f\n", costUSD)

		return nil
	},
}
