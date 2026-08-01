from __future__ import annotations

import json
import os
import platform
from html import escape
from pathlib import Path

from robocorp import browser
from robocorp.tasks import get_output_dir, task


ORDERS = [
    {"customer": "Northwind Co.", "status": "Ready", "total": 24.50},
    {"customer": "Contoso Ops", "status": "Queued", "total": 18.75},
    {"customer": "Fabrikam Lab", "status": "Ready", "total": 60.75},
]

EXPECTED_SUMMARY = {
    "page_title": "Browser Automation Smoke",
    "processed_orders": 3,
    "ready_orders": 2,
    "total_value": "$104.00",
}


def browser_config() -> dict:
    headless = os.getenv("HEADLESS", "true").lower() not in {"0", "false", "no"}
    requested_engine = os.getenv("BROWSER_ENGINE", "").lower()

    if requested_engine in {"chromium", "firefox", "webkit"}:
        return {"browser_engine": requested_engine, "headless": headless}

    if platform.system() == "Linux" and headless:
        return {"browser_engine": "firefox", "headless": headless}

    return {"browser_engine": "chromium", "headless": headless}


@task
def browser_smoke():
    output_dir = Path(get_output_dir())
    output_dir.mkdir(parents=True, exist_ok=True)

    fixture_path = write_fixture(output_dir / "browser-smoke.html")
    screenshot_path = output_dir / "browser-smoke.png"
    data_path = output_dir / "browser-smoke.json"

    config = browser_config()
    browser.configure(screenshot="only-on-failure", **config)

    page = browser.goto(fixture_path.resolve().as_uri())
    page.get_by_role("button", name="Build summary").click()

    summary = page.locator("[data-testid='summary']")
    summary.wait_for(state="visible")

    result = {
        "page_title": page.title(),
        "processed_orders": int(
            summary.locator("[data-testid='processed-orders']").inner_text()
        ),
        "ready_orders": int(summary.locator("[data-testid='ready-orders']").inner_text()),
        "total_value": summary.locator("[data-testid='total-value']").inner_text(),
    }

    if result != EXPECTED_SUMMARY:
        raise AssertionError(f"Unexpected browser summary: {result!r}")

    summary.screenshot(path=str(screenshot_path))
    data_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")

    print(f"Browser engine: {config['browser_engine']}")
    print(f"Processed orders: {result['processed_orders']}")
    print(f"Ready orders: {result['ready_orders']}")
    print(f"Total value: {result['total_value']}")
    print(f"Wrote {data_path}")
    print(f"Wrote {screenshot_path}")


def write_fixture(path: Path) -> Path:
    rows = "\n".join(
        f"""
        <tr data-status="{escape(order['status'])}" data-total="{order['total']:.2f}">
          <td>{escape(order['customer'])}</td>
          <td>{escape(order['status'])}</td>
          <td>${order['total']:.2f}</td>
        </tr>
        """.strip()
        for order in ORDERS
    )

    path.write_text(
        f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Browser Automation Smoke</title>
  <style>
    body {{
      color: #1f2937;
      font-family: Arial, sans-serif;
      margin: 2rem;
      max-width: 720px;
    }}
    table {{
      border-collapse: collapse;
      margin: 1rem 0;
      width: 100%;
    }}
    th,
    td {{
      border: 1px solid #d1d5db;
      padding: 0.5rem;
      text-align: left;
    }}
    button {{
      font-size: 1rem;
      padding: 0.5rem 0.75rem;
    }}
    [data-testid="summary"] {{
      border: 1px solid #2563eb;
      margin-top: 1rem;
      padding: 1rem;
    }}
  </style>
</head>
<body>
  <h1>Browser Automation Smoke</h1>
  <table>
    <thead>
      <tr>
        <th>Customer</th>
        <th>Status</th>
        <th>Total</th>
      </tr>
    </thead>
    <tbody>
      {rows}
    </tbody>
  </table>

  <button type="button" id="build-summary">Build summary</button>

  <section data-testid="summary" hidden>
    <h2>Order summary</h2>
    <p>Processed orders: <strong data-testid="processed-orders"></strong></p>
    <p>Ready orders: <strong data-testid="ready-orders"></strong></p>
    <p>Total value: <strong data-testid="total-value"></strong></p>
  </section>

  <script>
    document.querySelector("#build-summary").addEventListener("click", () => {{
      const rows = Array.from(document.querySelectorAll("tbody tr"));
      const ready = rows.filter((row) => row.dataset.status === "Ready").length;
      const total = rows.reduce((sum, row) => sum + Number(row.dataset.total), 0);
      const summary = document.querySelector('[data-testid="summary"]');

      summary.querySelector('[data-testid="processed-orders"]').textContent = rows.length;
      summary.querySelector('[data-testid="ready-orders"]').textContent = ready;
      summary.querySelector('[data-testid="total-value"]').textContent = `$${{total.toFixed(2)}}`;
      summary.hidden = false;
    }});
  </script>
</body>
</html>
""",
        encoding="utf-8",
    )
    return path
