#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
from datetime import date, datetime
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

import akshare as ak
import pandas as pd
import requests

ORIGINAL_PROXY_ENV = {
    key: value
    for key, value in os.environ.items()
    if key in {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} and value
}


class AKShareHandler(BaseHTTPRequestHandler):
    server_version = "AlphaThesisAKShare/0.1"

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        params = {k: v[-1] for k, v in parse_qs(parsed.query).items()}
        try:
            if parsed.path == "/health":
                self.write_json({"ok": True, "akshare_version": ak.__version__})
            elif parsed.path == "/news":
                self.write_json(handle_news(params))
            elif parsed.path == "/daily_quote":
                self.write_json(handle_daily_quote(params))
            elif parsed.path == "/search_symbol":
                self.write_json(handle_search_symbol(params))
            elif parsed.path == "/fetch_text":
                self.write_json(handle_fetch_text(params))
            else:
                self.write_json({"error": f"unknown path {parsed.path}"}, HTTPStatus.NOT_FOUND)
        except HTTPError as exc:
            self.write_json({"error": exc.message}, exc.status)
        except Exception as exc:  # noqa: BLE001
            self.write_json({"error": f"{type(exc).__name__}: {exc}"}, HTTPStatus.BAD_GATEWAY)

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"{self.address_string()} - {fmt % args}")

    def write_json(self, payload: Any, status: HTTPStatus = HTTPStatus.OK) -> None:
        body = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        self.send_response(status.value)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class HTTPError(Exception):
    def __init__(self, status: HTTPStatus, message: str):
        super().__init__(message)
        self.status = status
        self.message = message


def handle_news(params: dict[str, str]) -> list[dict[str, Any]]:
    symbol = required(params, "symbol")
    limit = parse_int(params.get("limit", "50"), "limit", minimum=1, maximum=100)
    try:
        df = ak.stock_news_em(symbol=symbol)
    except Exception as exc:  # noqa: BLE001
        raise HTTPError(HTTPStatus.BAD_GATEWAY, f"ak.stock_news_em failed: {exc}") from exc

    records = frame_records(df.head(limit))
    out: list[dict[str, Any]] = []
    for row in records:
        url = str(row.get("新闻链接") or "")
        title = str(row.get("新闻标题") or "")
        summary = str(row.get("新闻内容") or "")
        published_at = normalize_datetime(row.get("发布时间"))
        source_id = url or f"{symbol}|{title}|{published_at or ''}"
        out.append(
            {
                "source": "ak_news",
                "source_id": source_id,
                "source_url": url,
                "symbol": symbol,
                "title": title,
                "summary": summary,
                "published_at": published_at,
                "raw_payload": row,
            }
        )
    return out


def handle_daily_quote(params: dict[str, str]) -> dict[str, Any]:
    requested_symbol = required(params, "symbol")
    symbol = normalize_cn_symbol(requested_symbol)
    start = params.get("start", "19700101")
    end = params.get("end", "20500101")
    adjust = params.get("adjust", "")
    asset = params.get("asset", params.get("type", "auto")).strip().lower()
    if adjust not in {"", "qfq", "hfq"}:
        raise HTTPError(HTTPStatus.BAD_REQUEST, "adjust must be one of '', 'qfq', 'hfq'")
    if asset not in {"auto", "stock", "index"}:
        raise HTTPError(HTTPStatus.BAD_REQUEST, "asset must be one of 'auto', 'stock', 'index'")

    try:
        df = fetch_daily_quote_frame(symbol, start, end, adjust, asset)
    except Exception as exc:  # noqa: BLE001
        raise HTTPError(HTTPStatus.BAD_GATEWAY, f"ak daily quote failed: {exc}") from exc

    if df.empty:
        raise HTTPError(HTTPStatus.NOT_FOUND, "no daily quote rows returned")

    row = frame_records(df.tail(1))[0]
    return {
        "symbol": str(row.get("股票代码") or symbol),
        "date": normalize_date(row.get("日期")),
        "open": float(row["开盘"]),
        "close": float(row["收盘"]),
        "high": optional_float(row.get("最高")),
        "low": optional_float(row.get("最低")),
        "volume": optional_float(row.get("成交量")),
        "amount": optional_float(row.get("成交额")),
        "pct_change": optional_float(row.get("涨跌幅")),
        "turnover": optional_float(row.get("换手率")),
        "raw_payload": row,
    }


def normalize_cn_symbol(symbol: str) -> str:
    text = symbol.strip().upper()
    for suffix in (".SS", ".SH", ".SZ"):
        if text.endswith(suffix):
            text = text[: -len(suffix)]
            break
    if text.startswith(("SH", "SZ")) and len(text) == 8 and text[2:].isdigit():
        text = text[2:]
    return text


def fetch_daily_quote_frame(
    symbol: str,
    start: str,
    end: str,
    adjust: str,
    asset: str,
) -> pd.DataFrame:
    errors: list[str] = []
    if asset in {"auto", "stock"}:
        try:
            stock_df = ak.stock_zh_a_hist(
                symbol=symbol,
                period="daily",
                start_date=start,
                end_date=end,
                adjust=adjust,
            )
        except Exception as exc:  # noqa: BLE001
            errors.append(f"ak.stock_zh_a_hist: {exc}")
            try:
                stock_df = eastmoney_daily_quote_frame(symbol, start, end, adjust, "stock")
            except Exception as fallback_exc:  # noqa: BLE001
                errors.append(str(fallback_exc))
                stock_df = pd.DataFrame()
        if not stock_df.empty:
            return stock_df
        if asset == "stock" and errors:
            raise RuntimeError("; ".join(errors))

    try:
        index_df = ak.index_zh_a_hist(symbol=symbol, period="daily", start_date=start, end_date=end)
    except Exception as exc:  # noqa: BLE001
        errors.append(f"ak.index_zh_a_hist: {exc}")
        try:
            index_df = eastmoney_daily_quote_frame(symbol, start, end, adjust, "index")
        except Exception as fallback_exc:  # noqa: BLE001
            errors.append(str(fallback_exc))
            index_df = pd.DataFrame()
    if not index_df.empty and "股票代码" not in index_df.columns:
        index_df = index_df.copy()
        index_df["股票代码"] = symbol
    if index_df.empty and errors:
        raise RuntimeError("; ".join(errors))
    return index_df


def eastmoney_daily_quote_frame(
    symbol: str,
    start: str,
    end: str,
    adjust: str,
    asset: str,
) -> pd.DataFrame:
    """Fetch Eastmoney kline data directly with browser-like headers.

    Recent Eastmoney endpoints may close connections made by akshare's default
    bare requests. This fallback keeps the same response shape as AKShare.
    """
    adjust_dict = {"qfq": "1", "hfq": "2", "": "0"}
    market_ids = eastmoney_market_ids(symbol, asset)
    last_error = ""
    for market_id in market_ids:
        try:
            data = eastmoney_kline_json(symbol, market_id, start, end, adjust_dict[adjust])
        except Exception as exc:  # noqa: BLE001
            last_error = str(exc)
            continue
        klines = ((data.get("data") or {}).get("klines") or [])
        if klines:
            return kline_frame(symbol, klines, include_symbol=asset == "stock")
    try:
        return sina_daily_quote_frame(symbol, start, end, asset)
    except Exception as exc:  # noqa: BLE001
        last_error = f"{last_error}; sina fallback failed: {exc}" if last_error else f"sina fallback failed: {exc}"
    if last_error:
        raise RuntimeError(f"eastmoney fallback failed: {last_error}")
    return pd.DataFrame()


def eastmoney_market_ids(symbol: str, asset: str) -> list[int]:
    if asset == "stock":
        return [1] if symbol.startswith("6") else [0]
    # Eastmoney uses different market ids for indices; try the common ones in
    # the same spirit as ak.index_zh_a_hist's internal fallback.
    return [1, 0, 2, 47]


def eastmoney_kline_json(symbol: str, market_id: int, start: str, end: str, fqt: str) -> dict[str, Any]:
    url = "https://push2his.eastmoney.com/api/qt/stock/kline/get"
    params = {
        "fields1": "f1,f2,f3,f4,f5,f6",
        "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f116",
        "ut": "7eea3edcaed734bea9cbfc24409ed989",
        "klt": "101",
        "fqt": fqt,
        "secid": f"{market_id}.{symbol}",
        "beg": start,
        "end": end,
    }
    headers = {
        "Accept": "application/json, text/plain, */*",
        "Referer": "https://quote.eastmoney.com/",
        "User-Agent": "Mozilla/5.0 AlphaThesis/1.0",
    }
    resp = requests.get(url, params=params, headers=headers, timeout=15)
    resp.raise_for_status()
    return resp.json()


def kline_frame(symbol: str, klines: list[str], include_symbol: bool) -> pd.DataFrame:
    temp_df = pd.DataFrame([item.split(",") for item in klines])
    temp_df.columns = [
        "日期",
        "开盘",
        "收盘",
        "最高",
        "最低",
        "成交量",
        "成交额",
        "振幅",
        "涨跌幅",
        "涨跌额",
        "换手率",
        "股票代码",
    ][: len(temp_df.columns)]
    if include_symbol and "股票代码" not in temp_df.columns:
        temp_df["股票代码"] = symbol
    for col in ["开盘", "收盘", "最高", "最低", "成交量", "成交额", "振幅", "涨跌幅", "涨跌额", "换手率"]:
        if col in temp_df.columns:
            temp_df[col] = pd.to_numeric(temp_df[col], errors="coerce")
    return temp_df


def sina_daily_quote_frame(symbol: str, start: str, end: str, asset: str) -> pd.DataFrame:
    today = datetime.now().strftime("%Y%m%d")
    if start > today or end < today:
        return pd.DataFrame()

    code = sina_symbol(symbol, asset)
    url = f"https://hq.sinajs.cn/list={code}"
    headers = {
        "Referer": "https://finance.sina.com.cn",
        "User-Agent": "Mozilla/5.0 AlphaThesis/1.0",
    }
    resp = requests.get(url, headers=headers, timeout=15, proxies=original_requests_proxies())
    resp.raise_for_status()
    resp.encoding = "gb18030"
    payload = resp.text
    if '="' not in payload:
        return pd.DataFrame()
    csv = payload.split('="', 1)[1].split('";', 1)[0]
    fields = csv.split(",")
    if len(fields) < 32 or not fields[1] or not fields[3]:
        return pd.DataFrame()
    return pd.DataFrame([{
        "日期": fields[30],
        "股票代码": symbol,
        "开盘": pd.to_numeric(fields[1], errors="coerce"),
        "收盘": pd.to_numeric(fields[3], errors="coerce"),
        "最高": pd.to_numeric(fields[4], errors="coerce"),
        "最低": pd.to_numeric(fields[5], errors="coerce"),
        "成交量": pd.to_numeric(fields[8], errors="coerce"),
        "成交额": pd.to_numeric(fields[9], errors="coerce"),
        "涨跌幅": None,
        "换手率": None,
    }])


def sina_symbol(symbol: str, asset: str) -> str:
    if asset == "index":
        exchange = "sz" if symbol.startswith("399") else "sh"
    else:
        exchange = "sh" if symbol.startswith("6") else "sz"
    return exchange + symbol


def original_requests_proxies() -> dict[str, str] | None:
    proxy = (
        ORIGINAL_PROXY_ENV.get("HTTPS_PROXY")
        or ORIGINAL_PROXY_ENV.get("https_proxy")
        or ORIGINAL_PROXY_ENV.get("ALL_PROXY")
        or ORIGINAL_PROXY_ENV.get("all_proxy")
    )
    if not proxy:
        return None
    return {"http": proxy, "https": proxy}


def handle_search_symbol(params: dict[str, str]) -> list[dict[str, Any]]:
    """Search A-share stocks by Chinese company name.

    Tries SH (主板 + 科创板) and SZ exchanges in order; partial results are
    returned when some exchanges are unreachable.
    """
    query = required(params, "query")
    limit = parse_int(params.get("limit", "10"), "limit", minimum=1, maximum=50)

    frames = []
    for symbol in ("主板A股", "科创板"):
        try:
            df = ak.stock_info_sh_name_code(symbol=symbol)
            df = df[["证券代码", "证券简称"]].rename(columns={"证券代码": "code", "证券简称": "name"})
            frames.append(df)
        except Exception:  # noqa: BLE001
            pass
    try:
        sz = ak.stock_info_sz_name_code(symbol="A股列表")
        sz = sz[["A股代码", "A股简称"]].rename(columns={"A股代码": "code", "A股简称": "name"})
        frames.append(sz)
    except Exception:  # noqa: BLE001
        pass

    if not frames:
        return []

    combined = pd.concat(frames, ignore_index=True)
    mask = combined["name"].str.contains(query, na=False)
    matches = combined[mask].head(limit)
    out: list[dict[str, Any]] = []
    for _, row in matches.iterrows():
        out.append({
            "symbol": str(row["code"]),
            "company_name": str(row["name"]),
            "exchange": "cn",
            "currency": "CNY",
        })
    return out


def handle_fetch_text(params: dict[str, str]) -> dict[str, Any]:
    """Download a PDF from source_url and return extracted plain text.

    Only PDF content is handled here; callers should use the Go FullTextFetcher
    for HTML/plain-text URLs since that path needs no Python dependency.
    """
    source_url = required(params, "url")
    max_chars = parse_int(params.get("max_chars", "500000"), "max_chars", minimum=1000, maximum=2_000_000)

    import io
    import urllib.request

    req = urllib.request.Request(source_url, headers={"User-Agent": "Mozilla/5.0 AlphaThesis/1.0"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            raw = resp.read()
            content_type = resp.headers.get("Content-Type", "").lower()
    except Exception as exc:  # noqa: BLE001
        raise HTTPError(HTTPStatus.BAD_GATEWAY, f"fetch {source_url}: {exc}") from exc

    is_pdf = "pdf" in content_type or source_url.lower().endswith(".pdf")
    if not is_pdf:
        raise HTTPError(
            HTTPStatus.UNPROCESSABLE_ENTITY,
            f"unsupported content type '{content_type}' for {source_url}; only PDF is handled here",
        )

    text = _extract_pdf_text(raw, max_chars)
    return {"text": text, "source_url": source_url, "char_count": len(text)}


def _extract_pdf_text(raw: bytes, max_chars: int) -> str:
    try:
        import pdfplumber  # pip install pdfplumber
    except ImportError as exc:
        raise HTTPError(
            HTTPStatus.NOT_IMPLEMENTED,
            "pdfplumber is not installed; run: pip install pdfplumber",
        ) from exc

    import io

    parts: list[str] = []
    total = 0
    with pdfplumber.open(io.BytesIO(raw)) as pdf:
        for page in pdf.pages:
            page_text = page.extract_text() or ""
            parts.append(page_text)
            total += len(page_text)
            if total >= max_chars:
                break

    full_text = "\n".join(parts).strip()
    if not full_text:
        raise HTTPError(
            HTTPStatus.UNPROCESSABLE_ENTITY,
            "PDF appears to be image-based or empty (no extractable text)",
        )
    return full_text[:max_chars]


def frame_records(df: pd.DataFrame) -> list[dict[str, Any]]:
    clean = df.where(pd.notnull(df), None)
    return clean.to_dict(orient="records")


def required(params: dict[str, str], key: str) -> str:
    value = params.get(key, "").strip()
    if not value:
        raise HTTPError(HTTPStatus.BAD_REQUEST, f"missing required query parameter {key}")
    return value


def parse_int(value: str, name: str, minimum: int, maximum: int) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise HTTPError(HTTPStatus.BAD_REQUEST, f"{name} must be an integer") from exc
    if parsed < minimum or parsed > maximum:
        raise HTTPError(HTTPStatus.BAD_REQUEST, f"{name} must be between {minimum} and {maximum}")
    return parsed


def normalize_datetime(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, datetime):
        return value.isoformat(sep=" ")
    text = str(value).strip()
    return text or None


def normalize_date(value: Any) -> str:
    if isinstance(value, date):
        return value.isoformat()
    text = str(value).strip()
    if not text:
        raise HTTPError(HTTPStatus.BAD_GATEWAY, "daily quote row has empty date")
    return text


def optional_float(value: Any) -> float | None:
    if value is None:
        return None
    return float(value)


def clear_proxy_env() -> None:
    for key in [
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "http_proxy",
        "https_proxy",
        "all_proxy",
    ]:
        os.environ.pop(key, None)
    os.environ.setdefault("NO_PROXY", "*")
    os.environ.setdefault("no_proxy", "*")


def main() -> int:
    parser = argparse.ArgumentParser(description="AlphaThesis AKShare HTTP adapter")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8811)
    parser.add_argument("--keep-proxy", action="store_true", help="keep HTTP(S)_PROXY environment variables")
    args = parser.parse_args()

    if not args.keep_proxy:
        clear_proxy_env()

    server = ThreadingHTTPServer((args.host, args.port), AKShareHandler)
    print(f"AKShare adapter listening on http://{args.host}:{args.port}")
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
