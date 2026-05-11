#!/usr/bin/env python3
"""yfinance MCP server — news and daily price quotes for US/HK stocks.

Usage:
    python3 scripts/yfinance_mcp_server.py          # default port 8812
    YFINANCE_MCP_PORT=9000 python3 scripts/yfinance_mcp_server.py

Endpoint (streamable HTTP):
    http://localhost:8812/mcp
"""

import json
import os
import sys
from datetime import timezone

import yfinance as yf
from mcp.server.fastmcp import FastMCP

port = int(os.environ.get("YFINANCE_MCP_PORT", "8812"))
mcp = FastMCP("yfinance", host="0.0.0.0", port=port)


def to_internal_symbol(yf_symbol: str) -> str:
    """Convert yfinance's native ticker format to our internal canonical format.

    yfinance → internal
    ---------------------
    "0700.HK"   → "00700.HK"   (HK: zero-pad to 5 digits)
    "600036.SS" → "600036"     (A-share: strip exchange suffix)
    "000858.SZ" → "000858"     (A-share: strip exchange suffix)
    "AAPL"      → "AAPL"       (US: unchanged)
    """
    s = yf_symbol.strip().upper()
    if s.endswith(".HK"):
        num = s[:-3]
        try:
            s = f"{int(num):05d}.HK"
        except ValueError:
            pass
        return s
    if s.endswith(".SS") or s.endswith(".SZ"):
        return s[:-3]
    return s


def resolve_yf_symbol(symbol: str) -> str:
    """Convert internal symbol format to the ticker string yfinance expects.

    Rules
    -----
    HK stocks  : "00700.HK" → "0700.HK"  (strip extra leading zeros, keep 4 digits)
    A-shares   : "600036"   → "600036.SS" (starts with 6xx → Shanghai)
                 "000858"   → "000858.SZ" (starts with 0xx / 3xx → Shenzhen)
    US / other : pass through unchanged
    """
    s = symbol.strip().upper()

    # HK: ends with ".HK" — normalize to 4-digit numeric prefix
    if s.endswith(".HK"):
        num_part = s[:-3]          # e.g. "00700"
        try:
            s = f"{int(num_part):04d}.HK"
        except ValueError:
            pass  # keep original if not purely numeric
        return s

    # A-shares: exactly 6 digits, no dot
    if len(s) == 6 and s.isdigit():
        if s[0] == "6":
            return s + ".SS"      # Shanghai
        else:
            return s + ".SZ"      # Shenzhen (000, 002, 300, …)

    return s


@mcp.tool()
def search_symbol(query: str, max_results: int = 5) -> str:
    """Search for stock symbols by company name or ticker fragment.

    Returns a JSON array of candidates with keys:
        symbol (internal format), company_name, exchange, currency
    Use this to resolve uncertain company names before creating a thesis.
    """
    results = yf.Search(query, max_results=max_results)
    quotes = results.quotes or []
    candidates = []
    for q in quotes[:max_results]:
        raw_symbol = q.get("symbol", "")
        if not raw_symbol:
            continue
        candidates.append({
            "symbol":       to_internal_symbol(raw_symbol),
            "company_name": q.get("longname") or q.get("shortname") or "",
            "exchange":     q.get("exchange", ""),
            "currency":     q.get("currency", ""),
        })
    return json.dumps(candidates, ensure_ascii=False)


@mcp.tool()
def get_news(symbol: str, limit: int = 30) -> str:
    """Fetch recent news articles for a stock symbol via Yahoo Finance.

    Returns a JSON array of objects with keys:
        title, url, publisher, published_at (ISO-8601), summary
    """
    yf_symbol = resolve_yf_symbol(symbol)
    ticker = yf.Ticker(yf_symbol)
    raw = ticker.news or []
    results = []
    for item in raw[:limit]:
        content = item.get("content", {})
        canonical = content.get("canonicalUrl") or {}
        url = canonical.get("url", "") or content.get("url", "")
        pub_date = content.get("pubDate", "") or ""
        results.append({
            "title":        content.get("title", ""),
            "url":          url,
            "publisher":    (content.get("provider") or {}).get("displayName", ""),
            "published_at": pub_date,
            "summary":      content.get("summary", ""),
        })
    return json.dumps(results, ensure_ascii=False)


@mcp.tool()
def get_daily_quote(symbol: str) -> str:
    """Fetch the latest available daily OHLC for a stock.

    Returns a JSON object with keys: symbol, date (YYYY-MM-DD), open, close.
    Returns {"error": "..."} if data is unavailable.
    """
    yf_symbol = resolve_yf_symbol(symbol)
    ticker = yf.Ticker(yf_symbol)
    hist = ticker.history(period="5d")
    if hist.empty:
        return json.dumps({"error": f"no data for {symbol} (tried {yf_symbol})"})
    latest = hist.iloc[-1]
    date = hist.index[-1]
    if hasattr(date, "date"):
        date_str = str(date.date())
    else:
        date_str = str(date)[:10]
    return json.dumps({
        "symbol": symbol,
        "date":   date_str,
        "open":   float(latest["Open"]),
        "close":  float(latest["Close"]),
    })


if __name__ == "__main__":
    print(f"yfinance MCP server starting on port {port} ...", flush=True)
    mcp.run(transport="streamable-http")
