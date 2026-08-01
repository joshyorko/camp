# Python Browser Automation

Small RCC template for browser automation with `robocorp-browser`, which wraps
Playwright for Python tasks.

The included task is a deterministic smoke test: it writes a local HTML page,
opens it in a real headless browser, clicks a button, reads the rendered
summary, verifies the values, and saves artifacts under `output/`.

## Run From The Robot Root

All commands below assume your shell is already in this directory:

```bash
cd 02-python-browser
```

This is an RCC-contained template. On Bluefin/Linux, do not install project
Python packages or Playwright browsers on the host for this smoke test.

## Quick Smoke

Run the browser task:

```bash
rcc run -t BrowserExample
```

For an isolated RCC cache during local or CI verification:

```bash
ROBOCORP_HOME=/tmp/robot-templates-02-browser-rcc rcc run -t BrowserExample
```

Expected terminal lines include:

```text
Browser engine: firefox
Processed orders: 3
Ready orders: 2
Total value: $104.00
```

On non-Linux systems the default engine may be `chromium`. To force a browser
engine that Playwright supports:

```bash
BROWSER_ENGINE=chromium rcc run -t BrowserExample
BROWSER_ENGINE=firefox rcc run -t BrowserExample
```

To show the browser while developing locally:

```bash
HEADLESS=false rcc run -t BrowserExample
```

## Artifacts

After a successful run, check `output/`:

```text
output/browser-smoke.html   # generated local fixture page
output/browser-smoke.json   # fetched browser data
output/browser-smoke.png    # screenshot of the rendered summary
output/log.html             # Robocorp task log
```

The JSON artifact should contain:

```json
{
  "page_title": "Browser Automation Smoke",
  "processed_orders": 3,
  "ready_orders": 2,
  "total_value": "$104.00"
}
```

## First Files To Edit

- `tasks.py`: replace `ORDERS`, selectors, assertions, and artifact names with
  the browser workflow you want.
- `robot.yaml`: rename `BrowserExample` or add more RCC tasks.
- `conda.yaml`: add Python packages only when the browser task needs them.

Keep local/CI smoke tests pointed at stable pages or local fixtures. Avoid
authenticated sites and volatile public pages for the default template path.

## Development Commands

Inspect the RCC environment without running the task:

```bash
rcc ht vars -r robot.yaml
```

Run the configured task:

```bash
rcc run -t BrowserExample
```

## References

- [RCC recipes](https://github.com/joshyorko/rcc/blob/master/docs/recipes.md)
- [robocorp-browser](https://pypi.org/project/robocorp-browser/)
- [Playwright for Python](https://playwright.dev/python/docs/intro)
