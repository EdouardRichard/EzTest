/*
 * S8 UI acceptance probe for EzTest 0.2.1.
 *
 * Reads a JSON options file:
 *   { webBaseUrl, versionId, conversationId, designVersionId, token, user,
 *     outDir, timeoutMs }
 *
 * Drives the real Next.js UI with Playwright (executor's installed module),
 * checks the chat/thinking/tool/job surfaces, the model selector, the floating
 * chat state preservation, the three-layer design layout and the mindmap zoom
 * controls. Writes screenshots and s8-ui-dom.json; never prints the token.
 */
const fs = require("node:fs");
const path = require("node:path");

function writeJson(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(value, null, 2), "utf8");
}

async function main() {
  const optionsPath = process.argv[2];
  const options = JSON.parse(fs.readFileSync(optionsPath, "utf8"));
  let playwright;
  try {
    playwright = require("playwright");
  } catch (error) {
    throw new Error("cannot load playwright: " + error.message);
  }
  let browser;
  try {
    browser = await playwright.chromium.launch({ channel: "chrome", headless: true });
  } catch (chromeError) {
    browser = await playwright.chromium.launch({ headless: true });
  }
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
  });
  await context.addInitScript(
    ({ token, user }) => {
      try {
        if (token) sessionStorage.setItem("eztest.token", token);
        if (user) sessionStorage.setItem("eztest.user", JSON.stringify(user));
      } catch (error) {
        /* storage can be unavailable on about:blank */
      }
    },
    { token: options.token || "", user: options.user || null },
  );
  const page = await context.newPage();
  const consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => consoleErrors.push(String(error)));

  const checks = [];
  const observations = {};
  const screenshots = [];
  const check = (name, passed, detail) => {
    checks.push({ name, passed: Boolean(passed), detail: String(detail || "") });
  };
  const settle = (ms) => page.waitForTimeout(ms);
  const waitSelector = async (selector, timeoutMs) => {
    try {
      await page.waitForSelector(selector, { timeout: timeoutMs });
      return true;
    } catch (error) {
      return false;
    }
  };
  const shot = async (name) => {
    const file = path.join(options.outDir, name);
    await page.screenshot({ path: file, fullPage: false });
    screenshots.push(file);
  };

  try {
    /* ------------------------- chat / requirements ------------------------ */
    const chatUrl =
      options.webBaseUrl.replace(/\/+$/, "") +
      "/versions/" +
      encodeURIComponent(options.versionId) +
      "?tab=requirements&conversation=" +
      encodeURIComponent(options.conversationId);
    const chatResponse = await page.goto(chatUrl, {
      waitUntil: "domcontentloaded",
      timeout: options.timeoutMs || 60000,
    });
    const chatHttp = chatResponse ? chatResponse.status() : null;
    const headerFound = await waitSelector('[data-testid="version-header"]', 60000);
    const chatFound = await waitSelector('[data-testid="chat-panel"]', 60000);
    await settle(4000);
    check("version header and chat panel render", headerFound && chatFound, "http=" + chatHttp + " header=" + headerFound + " chat=" + chatFound);

    const redirectToLogin = page.url().includes("/login");
    check("authenticated UI session", !redirectToLogin, "final_url=" + page.url());

    const thinkingCount = await page.locator('[data-testid="thinking-block"]').count();
    const toolCount = await page.locator('[data-testid^="tool-call-"]').count();
    const jobCount = await page.locator('[data-testid^="job-card-"]').count();
    const jobProgressCount = await page.locator('[data-testid="job-progress"]').count();
    observations.chat = { thinkingCount, toolCount, jobCount, jobProgressCount };
    check("thinking is visible in the chat", thinkingCount > 0, "thinking_blocks=" + thinkingCount);
    check("tool calls are visible in the chat", toolCount > 0, "tool_call_parts=" + toolCount);
    check("job cards/progress are visible in the chat", jobCount > 0 || jobProgressCount > 0, "job_cards=" + jobCount + " progress=" + jobProgressCount);
    await shot("s8-chat-thinking.png");

    const modelSelector = page.locator('[data-testid="model-selector"]');
    const modelCount = await modelSelector.count();
    let modelOptions = [];
    let selectedModel = "";
    if (modelCount > 0) {
      modelOptions = await modelSelector.locator("option").allTextContents();
      selectedModel = await modelSelector.inputValue();
    }
    observations.model = { count: modelCount, options: modelOptions, selected: selectedModel };
    check("model selector is rendered with the actual model", modelCount > 0 && selectedModel.length > 0 && modelOptions.length > 0, "selected=" + selectedModel + " options=" + modelOptions.length);

    let switchedModel = "";
    if (modelCount > 0 && modelOptions.length >= 2) {
      const values = await modelSelector.locator("option").evaluateAll((nodes) => nodes.map((node) => node.value));
      const alternative = values.find((value) => value && value !== selectedModel);
      if (alternative) {
        await modelSelector.selectOption(alternative);
        await settle(2500);
        switchedModel = await modelSelector.inputValue();
        check("user-initiated model switch updates the selector", switchedModel === alternative, "selected=" + switchedModel + " expected=" + alternative);
        if (selectedModel) {
          await modelSelector.selectOption(selectedModel);
          await settle(2000);
          const restored = await modelSelector.inputValue();
          check("model selector can be restored to the original model", restored === selectedModel, "restored=" + restored);
        }
      } else {
        check("user-initiated model switch updates the selector", false, "no alternative model in the catalog");
      }
    } else {
      check("user-initiated model switch updates the selector", false, "the catalog exposes fewer than two selectable models");
    }

    /* --------------------------- floating panel --------------------------- */
    const draftText = "S8 floating draft " + Date.now();
    const composer = page.locator('[data-testid="composer-textarea"]');
    let draftPreserved = false;
    if ((await composer.count()) > 0) {
      await composer.fill(draftText);
      await page.click('[data-testid="chat-panel-expand"]');
      const dialogFound = await waitSelector('[data-testid="floating-chat-dialog"]', 20000);
      await settle(1200);
      const dialogComposer = page.locator('[data-testid="floating-chat-dialog"] [data-testid="composer-textarea"]');
      const valueInDialog = (await dialogComposer.count()) > 0 ? await dialogComposer.inputValue() : "";
      await shot("s8-floating-panel.png");
      await page.click('[data-testid="floating-chat-close"]');
      await settle(1200);
      const valueAfterClose = (await composer.count()) > 0 ? await composer.inputValue() : "";
      draftPreserved = dialogFound && valueInDialog === draftText && valueAfterClose === draftText;
      check("floating chat keeps the composer draft on expand/collapse", draftPreserved, "dialog=" + dialogFound + " in_dialog=" + JSON.stringify(valueInDialog) + " after_close=" + JSON.stringify(valueAfterClose));
      await composer.fill("");
    } else {
      check("floating chat keeps the composer draft on expand/collapse", false, "composer not found");
    }

    /* ------------------------------ design UI ----------------------------- */
    if (options.designVersionId) {
      const designUrl =
        options.webBaseUrl.replace(/\/+$/, "") +
        "/versions/" +
        encodeURIComponent(options.designVersionId) +
        "?tab=design";
      await page.goto(designUrl, { waitUntil: "domcontentloaded", timeout: options.timeoutMs || 60000 });
      const designTab = await waitSelector('[data-testid="design-tab"]', 60000);
      const layers = await waitSelector('[data-testid="design-layers"]', 60000);
      await settle(4000);
      const layerChat = await page.locator('[data-testid="design-layer-chat"]').count();
      const layerMindmap = await page.locator('[data-testid="design-layer-mindmap"]').count();
      const layerDetail = await page.locator('[data-testid="design-layer-detail"]').count();
      const mindmap = await page.locator('[data-testid="design-mindmap"]').count();
      observations.design = { designTab, layers, layerChat, layerMindmap, layerDetail, mindmap };
      check("three-layer design layout is present", designTab && layers && layerChat > 0 && layerMindmap > 0 && layerDetail > 0, "chat=" + layerChat + " mindmap=" + layerMindmap + " detail=" + layerDetail);
      check("mindmap canvas is rendered", mindmap > 0, "mindmap=" + mindmap);
      await shot("s8-three-layers.png");

      let zoomChanged = false;
      let transformBefore = "";
      let transformAfter = "";
      if (mindmap > 0) {
        const viewport = page.locator(".react-flow__viewport").first();
        transformBefore = (await viewport.count()) > 0 ? await viewport.getAttribute("style") : "";
        const zoomIn = page.locator('[data-testid="design-zoom-in"]');
        if ((await zoomIn.count()) > 0) {
          await zoomIn.click();
          await settle(1200);
          transformAfter = (await viewport.count()) > 0 ? await viewport.getAttribute("style") : "";
          zoomChanged = transformBefore !== transformAfter;
          await shot("s8-mindmap-zoom.png");
        }
      }
      check("mindmap zoom control changes the viewport", zoomChanged, "before=" + String(transformBefore).slice(0, 120) + " after=" + String(transformAfter).slice(0, 120));

      let focusOpened = false;
      const expand = page.locator('[data-testid="design-mindmap-expand"]');
      if ((await expand.count()) > 0) {
        await expand.click();
        focusOpened = await waitSelector('[data-testid="design-focus-dialog"]', 20000);
        if (focusOpened) {
          await settle(1000);
          await shot("s8-mindmap-focus.png");
          await page.click('[data-testid="design-focus-close"]');
        }
      }
      check("mindmap focus dialog opens with the workspace", focusOpened, "opened=" + focusOpened);
    } else {
      check("three-layer design layout is present", false, "no version with design cases was discovered");
      check("mindmap canvas is rendered", false, "no version with design cases was discovered");
      check("mindmap zoom control changes the viewport", false, "no version with design cases was discovered");
      check("mindmap focus dialog opens with the workspace", false, "no version with design cases was discovered");
    }
  } finally {
    await browser.close();
  }

  const failed = checks.filter((item) => !item.passed);
  writeJson(path.join(options.outDir, "s8-ui-dom.json"), {
    ok: failed.length === 0,
    checks,
    observations,
    consoleErrors,
    screenshots,
  });
  if (failed.length > 0) {
    process.exitCode = 1;
  }
}

main().catch((error) => {
  const optionsPath = process.argv[2] || "";
  try {
    const options = JSON.parse(fs.readFileSync(optionsPath, "utf8"));
    writeJson(path.join(options.outDir, "s8-ui-dom.json"), {
      ok: false,
      error: String((error && error.stack) || error),
      checks: [],
      consoleErrors: [],
      screenshots: [],
    });
  } catch (writeError) {
    /* ignore */
  }
  console.error(String((error && error.stack) || error));
  process.exit(1);
});
