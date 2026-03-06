package config

import (
	"encoding/json"
	"testing"
)

// TestPricingConfig computes various costs for models
func TestPricingConfigComputeCost(t *testing.T) {
	t.Run("compute_cost_standard_models", func(t *testing.T) {
		cfg := &PricingConfig{
			Models: map[string]ModelPrice{
				"claude-3-5-sonnet-20240620": {InputPer1K: 0.003, OutputPer1K: 0.015},
				"gpt-4o":                     {InputPer1K: 0.005, OutputPer1K: 0.015},
			},
			DefaultInputPer1K:  0.001,
			DefaultOutputPer1K: 0.003,
		}

		// Test matched model
		cost1 := cfg.ComputeCost("claude-3-5-sonnet-20240620", 1000, 2000)
		expected1 := (1000.0/1000)*0.003 + (2000.0/1000)*0.015 // 0.003 + 0.030 = 0.033
		if cost1 != expected1 {
			t.Errorf("Expected %f, got %f for sonnet model", expected1, cost1)
		}

		// Test unmatched model (defaults)
		cost2 := cfg.ComputeCost("unknown-model", 500, 1000)
		expected2 := (500.0/1000)*0.001 + (1000.0/1000)*0.003 // 0.0005 + 0.003 = 0.0035
		if cost2 != expected2 {
			t.Errorf("Expected %f, got %f for unknown model", expected2, cost2)
		}

		// Edge cases
		zeroCost := cfg.ComputeCost("claude-3-5-sonnet-20240620", 0, 0)
		if zeroCost != 0 {
			t.Errorf("Expected 0 for zero tokens, got %f", zeroCost)
		}
	})

	t.Run("compute_cost_edge_cases", func(t *testing.T) {
		cfg := &PricingConfig{
			Models: map[string]ModelPrice{
				"gpt-4o": {InputPer1K: 0.005, OutputPer1K: 0.015},
			},
			DefaultInputPer1K:  0, // Zero defaults
			DefaultOutputPer1K: 0,
		}

		// Zero costs
		cost := cfg.ComputeCost("nonexistent", 1000, 1000)
		if cost != 0 {
			t.Errorf("Expected 0 for zero-rate defaults, got %f", cost)
		}

		// Large token counts
		bigCost := cfg.ComputeCost("gpt-4o", 500000, 300000)      // 500k input, 300k output
		expected := (500000.0/1000)*0.005 + (300000.0/1000)*0.015 // 2.5 + 4.5 = 7.0
		if bigCost != expected {
			t.Errorf("Expected %f for large token counts, got %f", expected, bigCost)
		}
	})
}

// TestModelJsonMarshaling tests JSON marshaling functionality
func TestPricingConfigJsonMarshaling(t *testing.T) {
	t.Run("marshal_unmarshal_consistency", func(t *testing.T) {
		original := PricingConfig{
			Models: map[string]ModelPrice{
				"test-model": {
					InputPer1K:  0.003,
					OutputPer1K: 0.015,
				},
			},
			DefaultInputPer1K:  0.001,
			DefaultOutputPer1K: 0.003,
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Can't marshal PricingConfig: %v", err)
		}

		var unmarshaled PricingConfig
		err = json.Unmarshal(data, &unmarshaled)
		if err != nil {
			t.Fatalf("Can't unmarshal PricingConfig: %v", err)
		}

		if original.DefaultInputPer1K != unmarshaled.DefaultInputPer1K {
			t.Errorf("DefaultInputPer1K mismatch: expect %f, got %f",
				original.DefaultInputPer1K, unmarshaled.DefaultInputPer1K)
		}
	})
}
