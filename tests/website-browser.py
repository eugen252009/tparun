#!/usr/bin/env python3
"""Read-only Firefox layout smoke test against a locally running TPA Hub.

Requires selenium and Firefox. Does not submit forms or create accounts.
Authenticated page contracts are covered by website_layout_test.go.
"""
import argparse
from pathlib import Path
from urllib.parse import urlparse

from selenium import webdriver
from selenium.webdriver.common.by import By
from selenium.webdriver.common.keys import Keys
from selenium.webdriver.firefox.options import Options


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:18093")
    parser.add_argument("--screenshots", type=Path, required=True)
    args = parser.parse_args()
    if urlparse(args.url).hostname not in {"localhost", "127.0.0.1", "::1"}:
        parser.error("Use a local preview, not a production server")
    args.screenshots.mkdir(parents=True, exist_ok=True)
    options = Options()
    options.add_argument("-headless")
    options.enable_bidi = True
    paths = ["/", "/pricing", "/nano", "/docs", "/login", "/register",
             "/ops/login", "/ops/recover", "/missing-page"]
    with webdriver.Firefox(options=options) as browser:
        for width in (360, 768, 1440):
            browser.browsing_context.set_viewport(
                context=browser.current_window_handle,
                viewport={"width": width, "height": 1000},
            )
            for path in paths:
                browser.get(args.url.rstrip("/") + path)
                assert browser.execute_script("return window.innerWidth") == width
                assert len(browser.find_elements(By.TAG_NAME, "h1")) == 1, path
                assert len(browser.find_elements(By.TAG_NAME, "main")) == 1, path
                assert browser.execute_script(
                    "return document.documentElement.scrollWidth <= window.innerWidth"
                ), f"Page overflow at {width}: {path}"
                assert browser.execute_script(
                    "return [...document.querySelectorAll('input:not([type=hidden])')]"
                    ".every(input => input.labels.length > 0)"
                ), f"Unlabelled control: {path}"
                assert browser.execute_script(
                    "return [...document.querySelectorAll('a[href^=\"#\"]')]"
                    ".every(a => document.getElementById(a.hash.slice(1)))"
                ), f"Broken in-page link: {path}"
                if path == "/docs":
                    assert browser.execute_script(
                        "return [...document.querySelectorAll('pre code')].every(code => "
                        "code.textContent.split('\\n').every(line => !/^\\s{4,}(sudo|tpahub|curl|gpg|mema|bloom)/.test(line)))"
                    ), "Command block has unintended template indentation"
                browser.find_element(By.TAG_NAME, "body").send_keys(Keys.TAB)
                assert browser.switch_to.active_element.get_attribute("class") == "skip-link", path
                browser.switch_to.active_element.send_keys(Keys.ENTER)
                assert browser.switch_to.active_element.get_attribute("id") == "main", path
                browser.execute_script("document.activeElement.blur(); window.scrollTo(0, 0)")
                name = path.strip("/").replace("/", "-") or "home"
                browser.save_full_page_screenshot(str(args.screenshots / f"{name}-{width}.png"))
        print(f"PASS: {len(paths)} pages × 3 widths; labels, anchors, keyboard skip and page overflow")


if __name__ == "__main__":
    main()
