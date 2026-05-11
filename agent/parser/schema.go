package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	DirectionBullish = "bullish"
	DirectionBearish = "bearish"
	DirectionNeutral = "neutral"

	MarketUS = "us"
	MarketCN = "cn"
	MarketHK = "hk"

	AssumptionTypeBusiness    = "business"
	AssumptionTypeFinancial   = "financial"
	AssumptionTypeCompetitive = "competitive"
	AssumptionTypeRegulatory  = "regulatory"
	AssumptionTypeTechnical   = "technical"
	AssumptionTypeMacro       = "macro"
	AssumptionTypeValuation   = "valuation"
	AssumptionTypeOther       = "other"
)

var (
	keyInvalidChars = regexp.MustCompile(`[^a-z0-9_]+`)
	keyUnderscores  = regexp.MustCompile(`_+`)
)

// ParsedThesis is the structured output of ThesisParserAgent.
type ParsedThesis struct {
	Symbol      string             `json:"symbol"`
	CompanyName string             `json:"company_name"`
	Market      string             `json:"market"`
	Direction   string             `json:"direction"`
	CoreClaim   string             `json:"core_claim"`
	Assumptions []ParsedAssumption `json:"assumptions"`
}

// ParsedAssumption is one verifiable assumption that supports or breaks a thesis.
type ParsedAssumption struct {
	Key           string   `json:"key"`
	Text          string   `json:"text"`
	Type          string   `json:"type"`
	Verifiable    bool     `json:"verifiable"`
	Importance    float64  `json:"importance"`
	EvidenceHints []string `json:"evidence_hints"`
}

// PrettyString returns the parsed thesis as indented JSON for logs and tests.
func (t ParsedThesis) PrettyString() string {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", t)
	}
	return string(data)
}

// Normalize validates and canonicalizes a parsed thesis.
func (t *ParsedThesis) Normalize() error {
	t.Symbol = strings.ToUpper(strings.TrimSpace(t.Symbol))
	t.Symbol = normalizeCNSymbol(t.Symbol)
	t.CompanyName = strings.TrimSpace(t.CompanyName)
	t.Market = normalizeMarket(t.Market, t.Symbol)
	t.Direction = normalizeDirection(t.Direction)
	t.CoreClaim = strings.TrimSpace(t.CoreClaim)

	var errs []error
	if t.Symbol == "" {
		errs = append(errs, errors.New("symbol is required"))
	}
	if t.CoreClaim == "" {
		errs = append(errs, errors.New("core_claim is required"))
	}
	if t.Direction == "" {
		errs = append(errs, errors.New("direction must be bullish, bearish, or neutral"))
	}
	if len(t.Assumptions) == 0 {
		errs = append(errs, errors.New("at least one assumption is required"))
	}

	seenText := make(map[string]bool, len(t.Assumptions))
	seenKey := make(map[string]int, len(t.Assumptions))
	deduped := t.Assumptions[:0]
	for i := range t.Assumptions {
		a := &t.Assumptions[i]
		a.Text = strings.TrimSpace(a.Text)
		a.Type = normalizeAssumptionType(a.Type)
		a.Key = normalizeKey(a.Key)
		if a.Key == "" {
			a.Key = buildAssumptionKey(a.Text)
		}
		if a.Text == "" {
			errs = append(errs, fmt.Errorf("assumptions[%d].text is required", i))
		}
		if a.Type == "" {
			a.Type = AssumptionTypeOther
		}
		if a.Importance < 0 {
			a.Importance = 0
		}
		a.EvidenceHints = normalizeHints(a.EvidenceHints)

		textKey := strings.ToLower(a.Text)
		if seenText[textKey] {
			continue
		}
		seenText[textKey] = true

		base := a.Key
		seenKey[base]++
		if seenKey[base] > 1 {
			a.Key = fmt.Sprintf("%s_%d", base, seenKey[base])
		}
		deduped = append(deduped, *a)
	}
	t.Assumptions = deduped
	normalizeImportance(t.Assumptions)

	return errors.Join(errs...)
}

// normalizeCNSymbol strips exchange prefixes/suffixes that LLMs sometimes add
// to A-share codes, e.g. "600519.SH" → "600519", "SH600519" → "600519".
func normalizeCNSymbol(symbol string) string {
	for _, sfx := range []string{".SH", ".SZ", ".SS", ".BJ"} {
		if strings.HasSuffix(symbol, sfx) {
			return strings.TrimSuffix(symbol, sfx)
		}
	}
	for _, pfx := range []string{"SH", "SZ", "BJ"} {
		if strings.HasPrefix(symbol, pfx) {
			rest := strings.TrimPrefix(symbol, pfx)
			if len(rest) == 6 {
				allDigits := true
				for _, c := range rest {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					return rest
				}
			}
		}
	}
	return symbol
}

func normalizeMarket(market, symbol string) string {
	switch strings.ToLower(strings.TrimSpace(market)) {
	case MarketCN, "china":
		return MarketCN
	case MarketHK, "hongkong", "hong kong":
		return MarketHK
	case MarketUS, "usa":
		return MarketUS
	}
	// Fall back to symbol heuristic.
	upper := strings.ToUpper(symbol)
	if strings.HasSuffix(upper, ".HK") {
		return MarketHK
	}
	if len(symbol) == 6 {
		allDigits := true
		for _, c := range symbol {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return MarketCN
		}
	}
	return MarketUS
}

func normalizeDirection(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case DirectionBullish, "long", "buy", "positive":
		return DirectionBullish
	case DirectionBearish, "short", "sell", "negative":
		return DirectionBearish
	case DirectionNeutral, "mixed":
		return DirectionNeutral
	default:
		return ""
	}
}

func normalizeAssumptionType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AssumptionTypeBusiness:
		return AssumptionTypeBusiness
	case AssumptionTypeFinancial:
		return AssumptionTypeFinancial
	case AssumptionTypeCompetitive:
		return AssumptionTypeCompetitive
	case AssumptionTypeRegulatory:
		return AssumptionTypeRegulatory
	case AssumptionTypeTechnical:
		return AssumptionTypeTechnical
	case AssumptionTypeMacro:
		return AssumptionTypeMacro
	case AssumptionTypeValuation:
		return AssumptionTypeValuation
	case AssumptionTypeOther:
		return AssumptionTypeOther
	default:
		return AssumptionTypeOther
	}
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	key = keyInvalidChars.ReplaceAllString(key, "_")
	key = keyUnderscores.ReplaceAllString(key, "_")
	return strings.Trim(key, "_")
}

func buildAssumptionKey(text string) string {
	text = normalizeKey(text)
	if text == "" {
		return ""
	}
	parts := strings.Split(text, "_")
	if len(parts) > 6 {
		parts = parts[:6]
	}
	return strings.Join(parts, "_")
}

func normalizeHints(hints []string) []string {
	seen := make(map[string]bool, len(hints))
	out := make([]string, 0, len(hints))
	for _, hint := range hints {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			continue
		}
		key := strings.ToLower(hint)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, hint)
	}
	sort.Strings(out)
	return out
}

func normalizeImportance(assumptions []ParsedAssumption) {
	if len(assumptions) == 0 {
		return
	}
	var total float64
	for _, a := range assumptions {
		total += a.Importance
	}
	if total <= 0 {
		equal := 1 / float64(len(assumptions))
		for i := range assumptions {
			assumptions[i].Importance = equal
		}
		return
	}
	for i := range assumptions {
		assumptions[i].Importance = assumptions[i].Importance / total
	}
}
