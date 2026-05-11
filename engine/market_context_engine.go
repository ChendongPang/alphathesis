package engine

import "math"

// DefaultMarketETF is the benchmark used when no market ETF is specified.
const DefaultMarketETF = "SPY"
const CNMarketETF = "000300"
const HKMarketETF = "^HSI"

// Alert level thresholds based on |relative_return| (stock vs market).
const (
	alertHighThreshold   = 0.05  // ±5 %
	alertMediumThreshold = 0.02  // ±2 %
	alertLowThreshold    = 0.005 // ±0.5 %
)

// defaultSectorETFs maps common stock symbols to their primary sector ETF.
// The caller can override this map via MarketContextConfig.SectorETFs.
var defaultSectorETFs = map[string]string{
	// Semiconductors
	"NVDA": "SMH", "AMD": "SMH", "INTC": "SMH", "AVGO": "SMH",
	"QCOM": "SMH", "MU": "SMH", "AMAT": "SMH", "LRCX": "SMH",
	// Large-cap tech / software
	"AAPL": "XLK", "MSFT": "XLK", "GOOG": "XLK", "GOOGL": "XLK",
	"META": "XLK", "AMZN": "XLK", "ORCL": "XLK", "CRM": "XLK",
	// Cloud / SaaS
	"SNOW": "IGV", "DDOG": "IGV", "ZS": "IGV", "NET": "IGV",
	// Finance
	"JPM": "XLF", "BAC": "XLF", "GS": "XLF", "MS": "XLF",
	// Healthcare
	"JNJ": "XLV", "PFE": "XLV", "ABBV": "XLV", "UNH": "XLV",
	// Energy
	"XOM": "XLE", "CVX": "XLE", "COP": "XLE",
	// Consumer discretionary
	"TSLA": "XLY", "NKE": "XLY",
}

// ------------------------------------------------------------
// Config
// ------------------------------------------------------------

// MarketContextConfig controls MarketContextEngine behaviour.
type MarketContextConfig struct {
	// MarketETF is the broad-market benchmark symbol. Defaults to DefaultMarketETF.
	MarketETF string
	// SectorETFs overrides or extends the built-in symbol→sector ETF map.
	SectorETFs map[string]string
}

// MarketContextEngine computes market context metrics from OHLC price data.
// It is a pure, stateless calculation — no DB or network calls.
type MarketContextEngine struct {
	marketETF  string
	sectorETFs map[string]string
}

// NewMarketContextEngine creates a MarketContextEngine with the given config.
func NewMarketContextEngine(cfg MarketContextConfig) *MarketContextEngine {
	marketETF := cfg.MarketETF
	if marketETF == "" {
		marketETF = DefaultMarketETF
	}

	sectorETFs := make(map[string]string, len(defaultSectorETFs)+len(cfg.SectorETFs))
	for k, v := range defaultSectorETFs {
		sectorETFs[k] = v
	}
	for k, v := range cfg.SectorETFs {
		sectorETFs[k] = v
	}

	return &MarketContextEngine{
		marketETF:  marketETF,
		sectorETFs: sectorETFs,
	}
}

// SectorETFForSymbol returns the sector ETF ticker for a stock symbol, or ""
// if none is registered.
func (e *MarketContextEngine) SectorETFForSymbol(symbol string) string {
	return e.sectorETFs[symbol]
}

// MarketETF returns the configured broad-market benchmark ETF symbol.
func (e *MarketContextEngine) MarketETF() string {
	return e.marketETF
}

// MarketETFForMarket returns the benchmark ETF for the given market code.
func (e *MarketContextEngine) MarketETFForMarket(market string) string {
	switch market {
	case "cn":
		return CNMarketETF
	case "hk":
		return HKMarketETF
	default:
		return e.marketETF
	}
}

// ------------------------------------------------------------
// Input / Output types
// ------------------------------------------------------------

// PriceQuote is the open/close data for one symbol on one trading day.
type PriceQuote struct {
	Symbol string
	Open   float64
	Close  float64
}

// MarketContext is the computed market context for one thesis on one day.
// It is stored as JSON in daily_reports.market_context.
type MarketContext struct {
	Symbol string `json:"symbol"`

	StockOpen   float64 `json:"stock_open"`
	StockClose  float64 `json:"stock_close"`
	StockReturn float64 `json:"stock_return"`

	MarketETF    string  `json:"market_etf"`
	MarketOpen   float64 `json:"market_open"`
	MarketClose  float64 `json:"market_close"`
	MarketReturn float64 `json:"market_return"`

	SectorETF    string  `json:"sector_etf,omitempty"`
	SectorOpen   float64 `json:"sector_open,omitempty"`
	SectorClose  float64 `json:"sector_close,omitempty"`
	SectorReturn float64 `json:"sector_return,omitempty"`

	// RelativeReturn = stock_return - market_return.
	// Positive: stock outperformed the market.
	// Negative: stock underperformed the market.
	RelativeReturn float64 `json:"relative_return"`

	AlertLevel string `json:"alert_level"`
}

// ComputeInput groups the price quotes needed for one market context calculation.
type ComputeInput struct {
	Stock  PriceQuote
	Market PriceQuote
	// Sector is optional. Pass a zero-value PriceQuote (empty Symbol) to skip.
	Sector PriceQuote
}

// ------------------------------------------------------------
// Core computation
// ------------------------------------------------------------

// Compute calculates all market context metrics and returns a MarketContext.
// The caller is responsible for fetching the price quotes and for persisting
// the result.
func (e *MarketContextEngine) Compute(input ComputeInput) MarketContext {
	stockReturn := dailyReturn(input.Stock.Open, input.Stock.Close)
	marketReturn := dailyReturn(input.Market.Open, input.Market.Close)
	relativeReturn := stockReturn - marketReturn

	mc := MarketContext{
		Symbol:         input.Stock.Symbol,
		StockOpen:      input.Stock.Open,
		StockClose:     input.Stock.Close,
		StockReturn:    stockReturn,
		MarketETF:      input.Market.Symbol,
		MarketOpen:     input.Market.Open,
		MarketClose:    input.Market.Close,
		MarketReturn:   marketReturn,
		RelativeReturn: relativeReturn,
		AlertLevel:     alertLevel(relativeReturn),
	}

	if input.Sector.Symbol != "" {
		mc.SectorETF = input.Sector.Symbol
		mc.SectorOpen = input.Sector.Open
		mc.SectorClose = input.Sector.Close
		mc.SectorReturn = dailyReturn(input.Sector.Open, input.Sector.Close)
	}

	return mc
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

// dailyReturn computes (close - open) / open. Returns 0 when open is zero.
func dailyReturn(open, close float64) float64 {
	if open == 0 {
		return 0
	}
	return (close - open) / open
}

// alertLevel maps |relative_return| to an alert level string.
func alertLevel(relativeReturn float64) string {
	abs := math.Abs(relativeReturn)
	switch {
	case abs >= alertHighThreshold:
		return "high"
	case abs >= alertMediumThreshold:
		return "medium"
	case abs >= alertLowThreshold:
		return "low"
	default:
		return "none"
	}
}
