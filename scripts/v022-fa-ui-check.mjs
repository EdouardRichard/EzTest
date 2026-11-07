#!/usr/bin/env node
// EzTest v0.22 FA-6/FA-7 UI probe for R01-R06 and R07-R12 acceptance criteria.
//
// Usage:
//   node scripts/v022-fa-ui-check.mjs AC-R07-01 <screenshotDir>
//
// The script performs real Playwright operations against the SUT web app
// (http://localhost:5173) and prints one JSON object on stdout:
//
//   { acId, ok, observed, assertions:[{kind,ok,detail}], screenshot, screenshots }
//
// A missing browser / unexpected crash exits 1 with a JSON object whose
// fatal flag is true; the PowerShell probe records that as "blocked".
// Assertion failures are not crashes: they exit 0 with ok=false.

import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(SCRIPT_DIR, '..')
const requireFromExecutor = createRequire(path.join(REPO_ROOT, 'executor', 'package.json'))

const acId = process.argv[2]
const outDir = process.argv[3]

if (!acId || !outDir) {
  console.error('usage: node v022-fa-ui-check.mjs <acId> <outDir>')
  process.exit(2)
}

function readEnvFile() {
  const envPath = path.join(REPO_ROOT, '.env')
  const result = {}
  if (!fs.existsSync(envPath)) return result
  for (const line of fs.readFileSync(envPath, 'utf8').split(/\r?\n/)) {
    const match = line.match(/^\s*([A-Za-z0-9_]+)\s*=\s*(.*)$/)
    if (!match) continue
    let value = match[2].trim()
    if (value.length >= 2 && ((value[0] === '"' && value.at(-1) === '"') || (value[0] === "'" && value.at(-1) === "'"))) {
      value = value.slice(1, -1)
    }
    result[match[1]] = value
  }
  return result
}

const ENV_FILE = readEnvFile()
const PASSWORD = process.env.FA6_PASSWORD || ENV_FILE.SUT_PASSWORD || ''
const ADMIN = process.env.FA6_ADMIN_USERNAME || 'admin'
const AUTHOR = process.env.FA6_AUTHOR_USERNAME || 'author_e2e_login01'
const WEB = (process.env.FA6_WEB || 'http://localhost:5173').replace(/\/+$/, '')
const API = (process.env.FA6_API || 'http://localhost:8080').replace(/\/+$/, '')

const R04_POST_ID = process.env.FA6_R04_POST_ID || ''
const R04_CONTENT_POST_ID = process.env.FA6_R04_CONTENT_POST_ID || ''
const R05_POST_ID = process.env.FA6_R05_POST_ID || ''
const R05_TITLE = process.env.FA6_R05_TITLE || ''
const ADMIN_TOKEN = process.env.FA7_ADMIN_TOKEN || process.env.FA6_ADMIN_TOKEN || ''
const AUTHOR_TOKEN = process.env.FA7_AUTHOR_TOKEN || process.env.FA6_AUTHOR_TOKEN || ''
const R05_TAG_NAMES = toArray(safeJson(process.env.FA6_R05_TAG_NAMES_JSON, []))
const R05_NONPUBLIC = toArray(safeJson(process.env.FA6_R05_NONPUBLIC_JSON, []))

const R07_TITLE = process.env.FA7_R07_TITLE || ''
const R07_SLUG = process.env.FA7_R07_SLUG || ''
const R07_POST_ID = process.env.FA7_R07_POST_ID || ''
const R07_UPDATED_MARKER = process.env.FA7_R07_UPDATED_MARKER || ''
const R08_KEYWORD = process.env.FA7_R08_KEYWORD || ''
const R08_POST_ID = process.env.FA7_R08_POST_ID || ''
const R08_DRAFT_ID = process.env.FA7_R08_DRAFT_ID || ''
const R08_MARKER = process.env.FA7_R08_MARKER || ''
const R08_DRAFT_TOTAL = Number(process.env.FA7_R08_DRAFT_TOTAL || -1)
const R08_REVIEW_TOTAL = Number(process.env.FA7_R08_REVIEW_TOTAL || -1)
const R09_TAG_A_SLUG = process.env.FA7_R09_TAG_A_SLUG || ''
const R09_TAG_B_SLUG = process.env.FA7_R09_TAG_B_SLUG || ''
const R09_TAG_C_SLUG = process.env.FA7_R09_TAG_C_SLUG || ''
const R09_TAG_D_SLUG = process.env.FA7_R09_TAG_D_SLUG || ''
const R09_TAG_A_NAME = process.env.FA7_R09_TAG_A_NAME || ''
const R09_TAG_B_NAME = process.env.FA7_R09_TAG_B_NAME || ''
const R09_ALL_TOTAL = Number(process.env.FA7_R09_ALL_TOTAL || -1)
const R09_DUP_PATH = process.env.FA7_R09_DUP_PATH || ''
const R09_EXPECTED = Number(process.env.FA7_R09_EXPECTED || 2)
const R09_POST_IDS = toArray(safeJson(process.env.FA7_R09_POST_IDS, []))
const R11_POST_ID = process.env.FA7_R11_POST_ID || ''

const FIXTURES = path.join(REPO_ROOT, '.tmp', 'fa6', 'fixtures')
const PNG = path.join(FIXTURES, 'fa6-upload-200k.png')
const ZIP = path.join(FIXTURES, 'fa6-oversize.zip')
const EXE = path.join(FIXTURES, 'fa6-unsupported.exe')

const assertions = []
const screenshots = []
let shotIndex = 0

function safeJson(text, fallback) {
  if (!text) return fallback
  try { return JSON.parse(text) } catch { return fallback }
}

function toArray(value) {
  if (Array.isArray(value)) return value
  return value ? [value] : []
}

function assert(ok, detail) {
  assertions.push({ kind: 'ui', ok: Boolean(ok), detail: String(detail) })
  return Boolean(ok)
}

function logError(...args) {
  console.error(...args)
}

function logSutWrite(action, detail) {
  try {
    const file = path.join(REPO_ROOT, '.tmp', 'fa6', 'sut-writes.log')
    fs.mkdirSync(path.dirname(file), { recursive: true })
    fs.appendFileSync(file, `${new Date().toISOString()} | ui-${action} | ${detail}\r\n`, 'utf8')
  } catch { }
}

async function shot(page, slug) {
  shotIndex += 1
  const file = path.join(outDir, `${acId}-${String(shotIndex).padStart(2, '0')}-${slug}.png`)
  fs.mkdirSync(path.dirname(file), { recursive: true })
  await page.screenshot({ path: file, fullPage: true })
  screenshots.push(file)
  return file
}

async function apiJson(pathname, options = {}) {
  const headers = {}
  if (options.token) headers.authorization = `Bearer ${options.token}`
  let body
  if (options.body !== undefined) {
    headers['content-type'] = 'application/json'
    body = JSON.stringify(options.body)
  }
  const response = await fetch(`${API}${pathname}`, { method: options.method || 'GET', headers, body })
  let json = null
  try { json = await response.json() } catch { json = null }
  return { status: response.status, ok: response.ok, body: json }
}

async function apiLogin(account) {
  const result = await apiJson('/api/v1/auth/login', { method: 'POST', body: { account, password: PASSWORD } })
  if (!result.ok || !result.body?.data?.accessToken) {
    throw new Error(`login failed for ${account}: http=${result.status}`)
  }
  return result.body.data.accessToken
}

async function getAdminToken() {
  if (ADMIN_TOKEN) return ADMIN_TOKEN
  return apiLogin(ADMIN)
}

async function openLoginModal(page) {
  await page.goto(`${WEB}/`, { waitUntil: 'domcontentloaded' })
  await page.locator('.nav-actions button', { hasText: '登录' }).first().click()
  await page.waitForSelector('.login-modal', { timeout: 20000 })
}

async function loginThroughModal(page, account, password = PASSWORD) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await openLoginModal(page)
    await page.locator('.login-modal input[placeholder="用户名或邮箱"]').fill(account)
    await page.locator('.login-modal input[placeholder="请输入密码"]').fill(password)
    const responsePromise = page.waitForResponse((r) => r.url().includes('/api/v1/auth/login') && r.request().method() === 'POST', { timeout: 25000 })
    await page.locator('.login-modal form button[type=submit]').click()
    const response = await responsePromise
    if (response.status() === 429) {
      await page.waitForTimeout(61000)
      continue
    }
    if (!response.ok()) {
      const errorText = await page.locator('.login-modal .error').innerText().catch(() => '')
      throw new Error(`login failed http=${response.status()}: ${errorText}`)
    }
    await page.waitForSelector('.login-modal', { state: 'detached', timeout: 25000 })
    logSutWrite('login', `account=${account} ui-modal refresh-token-row-created`)
    return
  }
  throw new Error('login is rate-limited after retries')
}

async function waitForBlogCards(page) {
  await page.waitForFunction(() => {
    const list = document.querySelector('.post-list')
    if (!list) return false
    const loading = /正在加载文章/.test(list.textContent || '')
    return !loading && list.querySelectorAll('article.post-card').length > 0
  }, null, { timeout: 30000 })
  await page.waitForTimeout(250)
}

async function blogCardHrefs(page) {
  return page.locator('.post-list article.post-card').evaluateAll((cards) =>
    cards.map((card) => card.querySelector('a[href^="/posts/"]')?.getAttribute('href') || ''),
  )
}

async function firstCategory() {
  const result = await apiJson('/api/v1/categories')
  const items = result.body?.data
  if (!Array.isArray(items) || items.length === 0) throw new Error('SUT has no categories')
  return items[0]
}

async function openAdminPosts(page, token, viewport) {
  await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.admin-shell', { timeout: 30000 })
  await page.locator('.admin-sidebar button', { hasText: '文章管理' }).first().click()
  await page.waitForSelector('.filter-bar', { timeout: 20000 })
  await page.waitForFunction(() => !(document.body.innerText || '').includes('正在加载文章列表...'), null, { timeout: 20000 })
}

async function openEditor(page) {
  await page.goto(`${WEB}/editor`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.editor-page', { timeout: 30000 })
  await page.waitForSelector('[contenteditable="true"]', { timeout: 30000 })
  await page.waitForSelector('.settings-drawer', { timeout: 20000 })
  await page.waitForTimeout(600)
}

async function fillEditorIdentity(page, title, slug) {
  const inputs = page.locator('.settings-drawer input.input')
  await inputs.nth(0).fill(title)
  await inputs.nth(1).fill(slug)
}

async function waitArticle(page, id) {
  await page.waitForURL(new RegExp(`/posts/${id}(\\?|$)`), { timeout: 30000 })
  await page.waitForSelector('.article-page', { timeout: 30000 })
  await page.waitForTimeout(500)
}

async function submitBlogSearch(page, keyword) {
  const input = page.locator('.search-box input.input')
  await input.fill(keyword)
  await page.locator('.search-box button[type=submit]').click()
  await page.waitForFunction((value) => new URLSearchParams(location.search).get('q') === value, keyword, { timeout: 20000 })
  await page.waitForTimeout(700)
}

function countMatches(text, regex) {
  return (String(text || '').match(regex) || []).length
}

async function withPage(fn, options = {}) {
  const viewport = options.viewport || { width: 1440, height: 900 }
  const context = await browser.newContext({ viewport, deviceScaleFactor: options.deviceScaleFactor || 1 })
  if (options.token) {
    await context.addInitScript((value) => {
      try {
        localStorage.setItem('accessToken', value)
        localStorage.setItem('refreshToken', 'fa6-ui-check')
      } catch { }
    }, options.token)
  }
  const page = await context.newPage()
  page.setDefaultTimeout(30000)
  try {
    return await fn(page)
  } finally {
    await context.close()
  }
}

// ---------------------------------------------------------------------------
// Branches
// ---------------------------------------------------------------------------
const BRANCHES = {
  'AC-R01-01': async () => {
    await withPage(async (page) => {
      await openLoginModal(page)
      assert(await page.locator('.login-modal').isVisible(), 'login modal is visible after clicking 登录')
      await page.locator('.login-modal input[placeholder="用户名或邮箱"]').fill(ADMIN)
      await page.locator('.login-modal input[placeholder="请输入密码"]').fill(PASSWORD)
      await page.locator('.login-modal form button[type=submit]').click()
      await page.waitForSelector('.login-modal', { state: 'detached', timeout: 25000 })
      const navText = await page.locator('.nav-actions').innerText()
      assert(navText.includes('写文章'), `nav shows logged-in actions: ${navText.replace(/\s+/g, ' ').trim()}`)
      await shot(page, 'logged-in')
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => (document.querySelector('.nav-actions')?.textContent || '').includes('写文章'), null, { timeout: 25000 })
      const reloaded = await page.locator('.nav-actions').innerText()
      assert(reloaded.includes('写文章') && !reloaded.includes('登录'), 'session persists after full page reload')
      await shot(page, 'reloaded')
      return 'modal closed; nav=写文章; reload kept session'
    })
  },

  'AC-R01-02': async () => {
    await withPage(async (page) => {
      await openLoginModal(page)
      const submit = async (account, password) => {
        for (let attempt = 0; attempt < 3; attempt += 1) {
          await page.locator('.login-modal input[placeholder="用户名或邮箱"]').fill(account)
          await page.locator('.login-modal input[placeholder="请输入密码"]').fill(password)
          const response = page.waitForResponse((r) => r.url().includes('/api/v1/auth/login') && r.request().method() === 'POST', { timeout: 20000 })
          await page.locator('.login-modal form button[type=submit]').click()
          const resolved = await response
          if (resolved.status() === 429) {
            await page.waitForTimeout(61000)
            await openLoginModal(page)
            continue
          }
          await page.waitForTimeout(400)
          const errorText = await page.locator('.login-modal .error').innerText()
          const accountValue = await page.locator('.login-modal input[placeholder="用户名或邮箱"]').inputValue()
          return { status: resolved.status(), errorText, accountValue }
        }
        throw new Error('failed-login probe is rate-limited after retries')
      }
      const wrong = await submit(ADMIN, 'fa6-definitely-wrong-password')
      await shot(page, 'wrong-password')
      const missing = await submit(`fa6-no-such-user-${Date.now()}`, 'fa6-definitely-wrong-password')
      assert(wrong.status === 401 && missing.status === 401, `both failed logins return HTTP 401 (wrong=${wrong.status}, missing=${missing.status})`)
      assert(wrong.errorText === missing.errorText && wrong.errorText.length > 0, `uniform failure message: "${wrong.errorText}"`)
      const leaks = /不存在|未注册|账号错误|用户不存在/.test(wrong.errorText) || /不存在|未注册|账号错误|用户不存在/.test(missing.errorText)
      assert(!leaks, 'failure message does not reveal whether the account exists')
      assert(wrong.accountValue === ADMIN && missing.accountValue.includes('fa6-no-such-user'), 'account input keeps its submitted value after failure')
      await shot(page, 'uniform-error')
      return `wrong=http ${wrong.status} msg=${wrong.errorText}; missing=http ${missing.status} msg=${missing.errorText}`
    })
  },

  'AC-R01-03': async () => {
    await withPage(async (page) => {
      await loginThroughModal(page, 'admin@example.com')
      assert((await page.locator('.nav-actions').innerText()).includes('写文章'), 'email login closes modal and reaches logged-in state')
      await page.goto(`${WEB}/account`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.account-page', { timeout: 20000 })
      const username = await page.locator('.readonly-grid input').nth(0).inputValue()
      const email = await page.locator('input[type=email]').inputValue()
      assert(username === 'admin', `account page shows username: ${username}`)
      assert(email === 'admin@example.com', `account page shows bound email: ${email}`)
      await shot(page, 'email-login-account')
      return `email login ok; username=${username}; email=${email}`
    })
  },

  'AC-R02-01': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.login-modal', { timeout: 20000 })
      await page.waitForFunction(() => location.pathname === '/' && new URLSearchParams(location.search).get('login') === 'required', null, { timeout: 20000 })
      const modalVisible = await page.locator('.login-modal').isVisible()
      const url = new URL(page.url())
      assert(modalVisible, 'anonymous /admin opens the login modal')
      assert(url.pathname === '/' && url.searchParams.get('login') === 'required', `anonymous /admin lands on home: ${url.pathname}${url.search}`)
      await shot(page, 'admin-login-required')
      return `modal=${modalVisible}; url=${url.pathname}${url.search}`
    })
  },

  'AC-R02-02': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.login-modal', { timeout: 20000 })
      await page.locator('.login-modal input[placeholder="用户名或邮箱"]').fill(ADMIN)
      await page.locator('.login-modal input[placeholder="请输入密码"]').fill(PASSWORD)
      await page.locator('.login-modal form button[type=submit]').click()
      await page.waitForURL(/\/admin$/, { timeout: 25000 })
      await page.waitForFunction(() => (document.body.innerText || '').includes('文章管理'), null, { timeout: 25000 })
      assert(new URL(page.url()).pathname === '/admin', `after admin login the target page is restored: ${page.url()}`)
      assert((await page.locator('body').innerText()).includes('文章管理'), 'admin page content is rendered')
      await shot(page, 'admin-restored')
      return `url=${page.url()}; admin content rendered`
    })
  },

  'AC-R02-03': async () => {
    const check = async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => location.pathname === '/', null, { timeout: 20000 })
      await page.waitForTimeout(500)
      const url = new URL(page.url())
      const bodyText = await page.locator('body').innerText()
      assert(url.pathname === '/' && url.searchParams.get('forbidden') === '1', `author /admin is rejected to home: ${url.pathname}${url.search}`)
      assert(!bodyText.includes('墨栈 Admin') && !bodyText.includes('文章管理'), 'author cannot see admin article data')
      await shot(page, 'author-forbidden')
      return `url=${url.pathname}${url.search}; admin content absent`
    }
    if (AUTHOR_TOKEN) {
      await withPage(check, { token: AUTHOR_TOKEN })
    } else {
      await withPage(async (page) => {
        await loginThroughModal(page, AUTHOR)
        return check(page)
      })
    }
  },

  'AC-R02-04': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/fa6-nonexistent-page-${Date.now()}`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => location.pathname === '/', null, { timeout: 20000 })
      await page.waitForTimeout(300)
      const bodyText = await page.locator('body').innerText()
      assert(new URL(page.url()).pathname === '/', `unknown route redirects to home: ${page.url()}`)
      assert(bodyText.includes('首页') && bodyText.includes('墨栈'), 'home content is rendered after redirect')
      await shot(page, 'unknown-route-home')
      return `unknown route -> ${new URL(page.url()).pathname}; home rendered`
    })
  },

  'AC-R03-01': async () => {
    const apiResult = await apiJson('/api/v1/posts?page=1&pageSize=5')
    if (!apiResult.ok) throw new Error(`posts API failed: http=${apiResult.status}`)
    const total = Number(apiResult.body.data.total)
    const totalPages = Number(apiResult.body.data.totalPages)
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      await page.waitForFunction(() => document.querySelectorAll('.post-list article.post-card').length === 5, null, { timeout: 20000 })
      await page.waitForTimeout(300)
      const count = await page.locator('.post-list article.post-card').count()
      const pagination = await page.locator('.pagination').innerText()
      const aria = (await page.locator('.pager-input').getAttribute('aria-label')) || ''
      assert(count === 5, `page 1 renders 5 article cards (got ${count})`)
      assert(pagination.includes(`共 ${total} 篇`), `pagination shows total ${total} (got "${pagination.replace(/\s+/g, ' ').trim()}")`)
      assert(aria.includes(`当前第 1 页，共 ${totalPages} 页`), `pager input reports page 1/${totalPages} (got "${aria}")`)
      await shot(page, 'page-1')
      return `cards=${count}; total=${total}; pages=${totalPages}`
    })
  },

  'AC-R03-02': async () => {
    const page1 = await apiJson('/api/v1/posts?page=1&pageSize=5')
    const page2 = await apiJson('/api/v1/posts?page=2&pageSize=5')
    if (!page1.ok || !page2.ok) throw new Error('posts API failed for pagination probe')
    const page2First = String(page2.body.data.items[0]?.id || '')
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      await page.waitForTimeout(300)
      const firstPageHref = (await blogCardHrefs(page))[0] || ''
      await page.locator('.pagination button.pager-btn').nth(1).click()
      await page.waitForFunction(() => new URLSearchParams(location.search).get('page') === '2', null, { timeout: 20000 })
      await page.waitForFunction((id) => Boolean(document.querySelector(`.post-list article.post-card a[href*="${id}"]`)), page2First, { timeout: 25000 })
      await page.waitForTimeout(1800)
      const hrefs = await blogCardHrefs(page)
      const scrollY = await page.evaluate(() => window.scrollY)
      const listTop = await page.locator('.post-list').evaluate((el) => el.getBoundingClientRect().top)
      assert(hrefs.length === 5 && hrefs[0].includes(page2First), 'page 2 lists the API page-2 articles (items 6-10)')
      assert(hrefs[0] !== firstPageHref, 'page 2 first card differs from page 1 first card')
      assert(scrollY > 100 && Math.abs(listTop) < 150, `page scrolls back to the article list top (scrollY=${scrollY}, listTop=${listTop.toFixed(1)})`)
      await shot(page, 'page-2')
      return `page=2; cards=${hrefs.length}; scrollY=${scrollY}; listTop=${listTop.toFixed(1)}`
    })
  },

  'AC-R03-03': async () => {
    const clamped = await apiJson('/api/v1/posts?page=99&pageSize=5')
    if (!clamped.ok) throw new Error('posts API failed for page clamp probe')
    const lastPage = Number(clamped.body.data.page)
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      const input = page.locator('.pager-input')
      await input.fill('99')
      const valueAfterInput = await input.inputValue()
      assert(Number(valueAfterInput) === lastPage, `page input clamps 99 to last page ${lastPage} (got ${valueAfterInput})`)
      await page.locator('.pagination button.pager-jump').click()
      await page.waitForFunction((expected) => new URLSearchParams(location.search).get('page') === expected, String(lastPage), { timeout: 20000 })
      await page.waitForFunction(() => document.querySelectorAll('.post-list article.post-card').length > 0, null, { timeout: 20000 })
      await page.waitForTimeout(600)
      const count = await page.locator('.post-list article.post-card').count()
      const empty = await page.locator('.post-list .empty-state').count()
      assert(count > 0 && empty === 0, `last page renders a non-empty list (cards=${count}, emptyStates=${empty})`)
      const urlPage = new URL(page.url()).searchParams.get('page')
      assert(urlPage === String(lastPage), `URL carries the clamped page ${lastPage} (got ${urlPage})`)
      await shot(page, 'page-clamped')
      return `99 -> ${lastPage}; cards=${count}; urlPage=${urlPage}`
    })
  },

  'AC-R03-04': async () => {
    const category = await firstCategory()
    const categoryPage2 = await apiJson(`/api/v1/posts?page=2&pageSize=5&category=${encodeURIComponent(category.slug)}`)
    if (!categoryPage2.ok) throw new Error('category posts API failed')
    const expectedCount = Number(categoryPage2.body.data.items.length)
    const expectedTotalPages = Number(categoryPage2.body.data.totalPages)
    if (expectedTotalPages < 2) throw new Error(`category ${category.slug} has fewer than 2 pages`)
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?category=${encodeURIComponent(category.slug)}`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      await page.locator('.pagination button.pager-btn').nth(1).click()
      await page.waitForFunction((slug) => {
        const params = new URLSearchParams(location.search)
        return params.get('category') === slug && params.get('page') === '2'
      }, category.slug, { timeout: 20000 })
      await page.waitForFunction((expected) => document.querySelectorAll('.post-list article.post-card').length === expected, expectedCount, { timeout: 20000 })
      await page.waitForTimeout(500)
      const categoryTexts = await page.locator('.post-list article.post-card .category').allTextContents()
      const count = await page.locator('.post-list article.post-card').count()
      assert(count === expectedCount, `category page 2 renders ${expectedCount} cards (got ${count})`)
      assert(categoryTexts.length > 0 && categoryTexts.every((text) => text.includes(category.name)), `all cards keep category ${category.name}: ${categoryTexts.join(',')}`)
      const params = new URL(page.url()).searchParams
      assert(params.get('category') === category.slug && params.get('page') === '2', `URL combines category and page: ${page.url()}`)
      await shot(page, 'category-page-2')
      return `category=${category.slug}; page=2; cards=${count}`
    })
  },

  'AC-R04-01': async () => {
    const search = await apiJson('/api/v1/posts?page=1&pageSize=20&q=markdown')
    if (!search.ok) throw new Error('search API failed')
    const expectedIds = (search.body.data.items || []).map((item) => String(item.id))
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      const input = page.locator('.search-box input.input')
      await input.fill('markdown')
      await page.locator('.search-box button[type=submit]').click()
      await page.waitForFunction(() => new URLSearchParams(location.search).get('q') === 'markdown', null, { timeout: 20000 })
      await waitForBlogCards(page)
      await page.waitForTimeout(1200)
      const titles = await page.locator('.post-list article.post-card h3').allTextContents()
      const hrefs = await blogCardHrefs(page)
      const expectedId = R04_POST_ID || expectedIds[0] || ''
      assert(titles.some((title) => /markdown/i.test(title)), `lowercase search matches a title containing Markdown: ${titles.join(' | ')}`)
      assert(!expectedId || hrefs.some((href) => href.includes(expectedId)), `result contains the title-only fixture post ${expectedId}`)
      const params = new URL(page.url()).searchParams
      assert(params.get('q') === 'markdown', `URL keeps the search term: q=${params.get('q')}`)
      await shot(page, 'title-search')
      return `q=markdown; titles=${titles.length}; expectedId=${expectedId}`
    })
  },

  'AC-R04-02': async () => {
    const keyword = '草稿'
    const search = await apiJson(`/api/v1/posts?page=1&pageSize=20&q=${encodeURIComponent(keyword)}`)
    if (!search.ok) throw new Error('search API failed')
    const expectedIds = (search.body.data.items || []).map((item) => String(item.id))
    const expectedId = R04_CONTENT_POST_ID || expectedIds[0] || ''
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?q=${encodeURIComponent(keyword)}`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      await page.waitForTimeout(1200)
      const titles = await page.locator('.post-list article.post-card h3').allTextContents()
      const hrefs = await blogCardHrefs(page)
      assert(!expectedId || hrefs.some((href) => href.includes(expectedId)), `body-only match post ${expectedId} appears in results`)
      assert(titles.length > 0 && !titles.some((title) => title.includes(keyword)), `result titles do not contain ${keyword} (content-only match): ${titles.join(' | ')}`)
      await shot(page, 'body-search')
      return `q=${keyword}; results=${titles.length}; expectedId=${expectedId}`
    })
  },

  'AC-R04-03': async () => {
    const keyword = `fa6-no-match-${Date.now()}`
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?q=${encodeURIComponent(keyword)}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.post-list .empty-state button', { hasText: '清空搜索条件', timeout: 30000 })
      await page.waitForTimeout(400)
      const emptyText = await page.locator('.post-list .empty-state').innerText()
      const inputValue = await page.locator('.search-box input.input').inputValue()
      const clearButtons = await page.locator('.post-list .empty-state button', { hasText: '清空搜索条件' }).count()
      assert(emptyText.includes('没有找到相关文章'), `empty state message is shown: ${emptyText.replace(/\s+/g, ' ').trim()}`)
      assert(inputValue === keyword, `search box keeps the keyword: ${inputValue}`)
      assert(clearButtons > 0, 'empty state offers a clear-filters action')
      await shot(page, 'empty-state')
      return `empty state; keyword retained=${inputValue}; clearButtons=${clearButtons}`
    })
  },

  'AC-R04-04': async () => {
    const category = await firstCategory()
    const combined = await apiJson(`/api/v1/posts?page=1&pageSize=5&q=markdown&category=${encodeURIComponent(category.slug)}`)
    if (!combined.ok) throw new Error('combined search API failed')
    const expectedCount = Number(combined.body.data.items.length)
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?category=${encodeURIComponent(category.slug)}&q=markdown`, { waitUntil: 'domcontentloaded' })
      await waitForBlogCards(page)
      await page.waitForFunction((expected) => document.querySelectorAll('.post-list article.post-card').length === expected, expectedCount, { timeout: 20000 })
      await page.waitForTimeout(500)
      const count = await page.locator('.post-list article.post-card').count()
      const categoryTexts = await page.locator('.post-list article.post-card .category').allTextContents()
      const titles = await page.locator('.post-list article.post-card h3').allTextContents()
      const params = new URL(page.url()).searchParams
      assert(params.get('q') === 'markdown' && params.get('category') === category.slug, `URL keeps both q and category: ${page.url()}`)
      assert(count === expectedCount && count > 0, `combined result count matches API (${expectedCount}, got ${count})`)
      assert(categoryTexts.every((text) => text.includes(category.name)), `all combined results stay in category ${category.name}`)
      assert(titles.some((title) => /markdown/i.test(title)), 'combined results still satisfy the keyword')
      await shot(page, 'search-plus-category')
      return `q=markdown&category=${category.slug}; results=${count}`
    })
  },

  'AC-R05-01': async () => {
    if (!R05_POST_ID) throw new Error('FA6_R05_POST_ID is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R05_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(1500)
      const title = await page.locator('.article-head h1').innerText()
      const meta = await page.locator('.meta').innerText()
      const tags = await page.locator('.article-head .tags a').allTextContents()
      const tocCount = await page.locator('.toc button.level-2').count()
      const tables = await page.locator('.article-content table').count()
      const preBlocks = await page.locator('.article-content pre').count()
      const codeBlocks = await page.locator('.article-content code').count()
      const pageText = await page.locator('.article-page').innerText()
      assert(title === R05_TITLE, `title matches fixture: ${title}`)
      assert(meta.includes('系统管理员') && meta.includes('阅读'), `meta shows author and read count: ${meta.replace(/\s+/g, ' ').trim()}`)
      assert(tags.length >= R05_TAG_NAMES.length && R05_TAG_NAMES.every((name) => tags.some((tag) => tag.includes(name))), `tags are rendered: ${tags.join(',')}`)
      assert(tocCount === 3, `TOC shows the 3 level-2 headings (got ${tocCount})`)
      assert(tables >= 1 && preBlocks >= 1 && codeBlocks >= 1, `table/code blocks render (table=${tables}, pre=${preBlocks}, code=${codeBlocks})`)
      assert(!/发布时间|更新时间|发布于|更新于/.test(pageText), 'reading page does not display publish/update timestamps')
      await shot(page, 'article-detail')
      return `title=${title}; toc=${tocCount}; table=${tables}; tags=${tags.length}`
    })
  },

  'AC-R05-02': async () => {
    if (!R05_POST_ID) throw new Error('FA6_R05_POST_ID is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R05_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(1200)
      const before = await page.evaluate(() => window.scrollY)
      await page.locator('.toc button.level-2').nth(1).click()
      await page.waitForTimeout(1800)
      const after = await page.evaluate(() => window.scrollY)
      const rects = await page.locator('.article-content h2').evaluateAll((els) => els.map((el) => ({ text: el.textContent || '', top: el.getBoundingClientRect().top })))
      assert(after > before + 100, `clicking TOC item 2 scrolls the page (before=${before}, after=${after})`)
      assert(rects.length >= 2 && Math.abs(rects[1].top) < 220, `second heading is near the viewport top (top=${rects.length >= 2 ? rects[1].top.toFixed(1) : 'n/a'})`)
      await shot(page, 'toc-jump')
      return `scroll ${before} -> ${after}; heading2 top=${rects.length >= 2 ? rects[1].top.toFixed(1) : 'n/a'}`
    })
  },

  'AC-R05-03': async () => {
    const entries = Array.isArray(R05_NONPUBLIC) ? R05_NONPUBLIC : []
    if (entries.length === 0) throw new Error('FA6_R05_NONPUBLIC_JSON is empty')
    await withPage(async (page) => {
      const details = []
      for (const entry of entries) {
        await page.goto(`${WEB}/posts/${entry.id}`, { waitUntil: 'domcontentloaded' })
        await page.waitForTimeout(1500)
        const emptyTexts = await page.locator('.empty-state').allTextContents()
        const articleCount = await page.locator('.article-page').count()
        const bodyText = await page.locator('body').innerText()
        const messageShown = emptyTexts.some((text) => text.includes('文章不存在或已下架'))
        const titleLeaked = entry.title ? bodyText.includes(entry.title) : false
        assert(messageShown, `${entry.status} article ${entry.id} shows 文章不存在或已下架`)
        assert(articleCount === 0 && !titleLeaked, `${entry.status} article does not leak title/body`)
        details.push(`${entry.status}:${messageShown && articleCount === 0 && !titleLeaked}`)
        await shot(page, `nonpublic-${entry.status}`)
      }
      return details.join('; ')
    })
  },

  'AC-R05-04': async () => {
    if (!R05_POST_ID) throw new Error('FA6_R05_POST_ID is not set')
    const token = await getAdminToken()
    let adminResult
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R05_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(1500)
      const count = await page.locator('.edit-link').count()
      const text = count > 0 ? await page.locator('.edit-link').innerText() : ''
      const href = count > 0 ? await page.locator('.edit-link').getAttribute('href') : ''
      assert(count === 1 && text.includes('编辑文章'), `admin sees one edit entry: ${text}`)
      assert(String(href || '').includes(`/editor/${R05_POST_ID}`), `edit entry links to the editor: ${href}`)
      await shot(page, 'edit-entry-admin')
      adminResult = { count, text, href }
    }, { token })
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R05_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(800)
      const anonymousCount = await page.locator('.edit-link').count()
      assert(anonymousCount === 0, 'anonymous visitor does not see the edit entry')
      return { anonymousCount }
    })
    return `admin edit entry=${adminResult.text}; anon=0`
  },

  'AC-R06-01': async () => {
    if (!fs.existsSync(PNG)) throw new Error(`missing fixture ${PNG}`)
    const token = await getAdminToken()
    await withPage(async (page) => {
      await page.goto(`${WEB}/editor`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('[contenteditable="true"]', { timeout: 30000 })
      await page.waitForTimeout(1200)
      if (await page.locator('.settings-drawer.open').count()) {
        await page.locator('.settings-drawer-toggle').click()
        await page.waitForTimeout(500)
      }
      const editor = page.locator('[contenteditable="true"]').first()
      const secondLine = editor.locator('.cm-line', { hasText: '这是一段' }).first()
      await secondLine.click()
      await page.keyboard.press('End')
      await page.keyboard.type(' MARKER-CURSOR')
      await page.waitForTimeout(400)
      const chooserPromise = page.waitForEvent('filechooser', { timeout: 10000 })
      await page.locator('.cherry-toolbar-image').click()
      const chooser = await chooserPromise
      await chooser.setFiles(PNG)
      await page.waitForFunction(() => /!\[[^\]]*\]\(\/uploads\/[^)]+\)/.test(document.querySelector('[contenteditable="true"]')?.innerText || ''), null, { timeout: 30000 })
      await page.waitForTimeout(800)
      const text = await editor.innerText()
      const markerIndex = text.indexOf('MARKER-CURSOR')
      const uploadIndex = text.indexOf('/uploads/')
      const imageCount = await page.locator('.cherry-host img').count()
      assert(markerIndex >= 0, 'cursor marker typed into the second paragraph')
      assert(uploadIndex > markerIndex, `image reference inserted at the cursor (marker=${markerIndex}, upload=${uploadIndex})`)
      assert(/!\[[^\]]*\]\(\/uploads\/[^)]+\)/.test(text), 'markdown image reference uses the site upload URL')
      assert(imageCount >= 1, `uploaded image is visible in the editor preview (imgs=${imageCount})`)
      const uploadedUrl = (text.match(/!\[[^\]]*\]\((\/uploads\/[^)]+)\)/) || [])[1] || ''
      if (uploadedUrl) logSutWrite('upload', `AC-R06-01 usage=post-content url=${uploadedUrl}`)
      await shot(page, 'image-inserted')
      return `marker=${markerIndex}; upload=${uploadIndex}; imgs=${imageCount}`
    }, { token })
  },

  'AC-R06-02': async () => {
    if (!fs.existsSync(ZIP) || !fs.existsSync(EXE)) throw new Error('missing upload failure fixtures')
    const token = await getAdminToken()
    await withPage(async (page) => {
      await page.goto(`${WEB}/editor`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('[contenteditable="true"]', { timeout: 30000 })
      await page.waitForTimeout(1200)
      if (await page.locator('.settings-drawer.open').count()) {
        await page.locator('.settings-drawer-toggle').click()
        await page.waitForTimeout(500)
      }
      const editor = page.locator('[contenteditable="true"]').first()
      const before = await editor.innerText()

      const zipChooserPromise = page.waitForEvent('filechooser', { timeout: 10000 })
      await page.locator('.cherry-toolbar-phone').click()
      const zipChooser = await zipChooserPromise
      await zipChooser.setFiles(ZIP)
      await page.waitForSelector('.toast-item.error', { timeout: 40000 })
      const zipToast = await page.locator('.toast-item.error p').last().innerText()
      await page.waitForTimeout(500)
      const afterZip = await editor.innerText()
      const zipOk = /文件上传失败/.test(zipToast) && afterZip === before && !afterZip.includes('/uploads/')
      assert(zipOk, `oversized attachment shows a failure toast and inserts nothing (toast="${zipToast.replace(/\s+/g, ' ').trim()}")`)
      await shot(page, 'oversize-rejected')

      const exeChooserPromise = page.waitForEvent('filechooser', { timeout: 10000 })
      await page.locator('.cherry-toolbar-image').click()
      const exeChooser = await exeChooserPromise
      const uploadResponse = page.waitForResponse((r) => r.url().includes('/api/v1/uploads') && r.request().method() === 'POST', { timeout: 30000 }).catch(() => null)
      await exeChooser.setFiles(EXE)
      await uploadResponse
      await page.waitForSelector('.toast-item.error', { timeout: 30000 })
      const exeToast = await page.locator('.toast-item.error p').last().innerText()
      await page.waitForTimeout(500)
      const afterExe = await editor.innerText()
      const exeOk = /文件上传失败/.test(exeToast) && afterExe === before && !afterExe.includes('/uploads/')
      assert(exeOk, `unsupported type shows a failure toast and inserts nothing (toast="${exeToast.replace(/\s+/g, ' ').trim()}")`)
      await shot(page, 'unsupported-rejected')
      return `zipToast="${zipToast.replace(/\s+/g, ' ').trim()}"; exeToast="${exeToast.replace(/\s+/g, ' ').trim()}"`
    }, { token })
  },

  'AC-R06-03': async () => {
    if (!fs.existsSync(PNG)) throw new Error(`missing fixture ${PNG}`)
    const token = await getAdminToken()
    await withPage(async (page) => {
      await page.goto(`${WEB}/editor`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('[contenteditable="true"]', { timeout: 30000 })
      await page.waitForTimeout(1200)
      if (!(await page.locator('.settings-drawer.open').count())) {
        await page.locator('.settings-drawer-toggle').click()
        await page.waitForTimeout(500)
      }
      const coverInput = page.locator('.settings-drawer input[type=file]')
      await coverInput.setInputFiles(PNG)
      await page.waitForSelector('img[alt="文章封面预览"]', { timeout: 30000 })
      const preview = page.locator('img[alt="文章封面预览"]')
      const src = (await preview.getAttribute('src')) || ''
      const clearButton = page.locator('button', { hasText: '清空封面' })
      assert(src.includes('/uploads/'), `cover preview uses the uploaded asset: ${src}`)
      assert(await clearButton.count() === 1, 'clear-cover button is shown after upload')
      if (src) logSutWrite('upload', `AC-R06-03 usage=post-cover url=${src}`)
      await shot(page, 'cover-preview')
      await clearButton.click()
      await page.waitForTimeout(400)
      const previewAfter = await page.locator('img[alt="文章封面预览"]').count()
      const clearAfter = await page.locator('button', { hasText: '清空封面' }).count()
      assert(previewAfter === 0 && clearAfter === 0, 'clearing the cover removes the preview and the clear button')
      await shot(page, 'cover-cleared')
      return `cover src=${src}; cleared preview=${previewAfter}`
    }, { token })
  },

  'AC-R07-01': async () => {
    if (!R07_TITLE || !R07_SLUG) throw new Error('FA7_R07_TITLE/FA7_R07_SLUG are not set')
    await withPage(async (page) => {
      await openEditor(page)
      await fillEditorIdentity(page, R07_TITLE, R07_SLUG)
      await page.locator('.actions button', { hasText: '发布文章' }).click()
      await page.waitForURL(/\/posts\/[0-9a-f-]+$/, { timeout: 30000 })
      const url = new URL(page.url())
      assert(!url.searchParams.has('preview'), `publish URL has no preview marker: ${url.pathname}${url.search}`)
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(600)
      const title = await page.locator('.article-head h1').innerText()
      assert(title === R07_TITLE, `published detail shows the article title: ${title}`)
      const postId = url.pathname.split('/').pop()
      await shot(page, 'published-public')
      let anonymousTitle = ''
      await withPage(async (anon) => {
        await anon.goto(page.url(), { waitUntil: 'domcontentloaded' })
        await anon.waitForSelector('.article-page', { timeout: 30000 })
        await anon.waitForTimeout(500)
        anonymousTitle = await anon.locator('.article-head h1').innerText()
        assert(anonymousTitle === R07_TITLE, `anonymous visitor can read the same article: ${anonymousTitle}`)
        await shot(anon, 'anonymous-readable')
      })
      return `postId=${postId}; url=${url.pathname}; anonymousTitle=${anonymousTitle}`
    }, { token: AUTHOR_TOKEN })
  },

  'AC-R07-02': async () => {
    if (!R07_POST_ID || !R07_UPDATED_MARKER) throw new Error('FA7_R07_POST_ID/FA7_R07_UPDATED_MARKER are not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R07_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(800)
      const content = await page.locator('.article-content').innerText()
      assert(content.includes(R07_UPDATED_MARKER), `reading page shows the updated content marker: ${R07_UPDATED_MARKER}`)
      const bodyText = await page.locator('.article-page').innerText()
      assert(!/发布时间|更新时间|发布于|更新于/.test(bodyText), 'reading page does not display publish/update timestamps')
      await shot(page, 'updated-content')
      return `updated marker visible on /posts/${R07_POST_ID}`
    })
  },

  'AC-R07-03': async () => {
    if (!R07_TITLE || !R07_SLUG) throw new Error('FA7_R07_TITLE/FA7_R07_SLUG are not set')
    await withPage(async (page) => {
      await openEditor(page)
      await fillEditorIdentity(page, R07_TITLE, R07_SLUG)
      let postRequests = 0
      await page.route('**/api/v1/posts', async (route) => {
        if (route.request().method() === 'POST') {
          postRequests += 1
          await new Promise((resolve) => setTimeout(resolve, 2500))
        }
        await route.continue()
      })
      const publish = page.locator('.actions button', { hasText: '发布文章' })
      const draft = page.locator('.actions button', { hasText: '保存草稿' })
      const settingsSave = page.locator('.settings-drawer button[type=submit]')
      await publish.click()
      await page.waitForTimeout(600)
      const publishDisabled = await publish.isDisabled()
      const draftDisabled = await draft.isDisabled()
      const settingsDisabled = await settingsSave.isDisabled()
      assert(publishDisabled && draftDisabled, `save and publish buttons are disabled while saving (publish=${publishDisabled}, draft=${draftDisabled})`)
      assert(settingsDisabled, `settings save button is disabled while saving (${settingsDisabled})`)
      await publish.click({ force: true, timeout: 1200 }).catch(() => {})
      await draft.click({ force: true, timeout: 1200 }).catch(() => {})
      await page.waitForTimeout(400)
      assert(postRequests === 1, `only one save request was issued while the buttons were disabled (got ${postRequests})`)
      await shot(page, 'saving-disabled')
      await page.waitForURL(/\/posts\/[0-9a-f-]+$/, { timeout: 30000 })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      assert(postRequests === 1, `still exactly one save request after navigation (got ${postRequests})`)
      const postId = new URL(page.url()).pathname.split('/').pop()
      await shot(page, 'published-once')
      return `postId=${postId}; requests=${postRequests}; disabled=${publishDisabled},${draftDisabled},${settingsDisabled}`
    }, { token: AUTHOR_TOKEN })
  },

  'AC-R08-01': async () => {
    const token = await getAdminToken()
    await withPage(async (page) => {
      await openAdminPosts(page, token)
      const selects = page.locator('.filter-bar select')
      const statusSelect = selects.nth(0)
      const reviewSelect = selects.nth(1)
      const statusResponse = page.waitForResponse((r) => {
        if (r.request().method() !== 'GET' || !r.url().includes('/api/v1/admin/posts')) return false
        return new URL(r.url()).searchParams.get('status') === 'draft'
      }, { timeout: 20000 })
      await statusSelect.selectOption('draft')
      const statusResp = await statusResponse
      const statusJson = await statusResp.json()
      const statusTotal = Number(statusJson?.data?.total)
      await page.waitForTimeout(700)
      const statuses = await page.locator('.table-wrap tbody tr').evaluateAll((rows) => rows.map((row) => (row.children[4]?.textContent || '').trim()))
      assert(statuses.length > 0 && statuses.every((text) => text === '草稿'), `status=draft rows are all 草稿: ${statuses.join(',')}`)
      const statusQuery = new URL(statusResp.url()).searchParams
      assert((statusQuery.get('page') || '1') === '1', `status filter resets to page 1: ${statusResp.url()}`)
      const statusTotalText = await page.locator('.pagination-total').innerText()
      assert(statusTotalText.includes(`共 ${statusTotal} 条`), `status filter pagination total is ${statusTotal}: ${statusTotalText}`)
      if (R08_DRAFT_TOTAL >= 0) assert(statusTotal === R08_DRAFT_TOTAL, `status total matches probe API total ${R08_DRAFT_TOTAL} (got ${statusTotal})`)
      await shot(page, 'filter-status-draft')

      const reviewResponse = page.waitForResponse((r) => {
        if (r.request().method() !== 'GET' || !r.url().includes('/api/v1/admin/posts')) return false
        const params = new URL(r.url()).searchParams
        return params.get('reviewStatus') === 'approved' && params.get('status') === 'draft'
      }, { timeout: 20000 })
      await reviewSelect.selectOption('approved')
      const reviewResp = await reviewResponse
      const reviewJson = await reviewResp.json()
      const reviewTotal = Number(reviewJson?.data?.total)
      await page.waitForTimeout(700)
      const reviews = await page.locator('.table-wrap tbody tr').evaluateAll((rows) => rows.map((row) => (row.children[6]?.textContent || '').trim()))
      assert(reviews.length > 0 && reviews.every((text) => text === '已通过'), `reviewStatus=approved rows are all 已通过: ${reviews.join(',')}`)
      const reviewTotalText = await page.locator('.pagination-total').innerText()
      assert(reviewTotalText.includes(`共 ${reviewTotal} 条`), `combined filter pagination total is ${reviewTotal}: ${reviewTotalText}`)
      if (R08_REVIEW_TOTAL >= 0) assert(reviewTotal === R08_REVIEW_TOTAL, `combined total matches probe API total ${R08_REVIEW_TOTAL} (got ${reviewTotal})`)
      await shot(page, 'filter-review-approved')
      return `draft rows=${statuses.length}, total=${statusTotal}; draft+approved rows=${reviews.length}, total=${reviewTotal}`
    }, { token })
  },

  'AC-R08-02': async () => {
    if (!R08_KEYWORD) throw new Error('FA7_R08_KEYWORD is not set')
    const token = await getAdminToken()
    await withPage(async (page) => {
      await openAdminPosts(page, token)
      const input = page.locator('.filter-bar input.input')
      const started = Date.now()
      const responsePromise = page.waitForResponse((r) => {
        if (r.request().method() !== 'GET' || !r.url().includes('/api/v1/admin/posts')) return false
        return new URL(r.url()).searchParams.get('q') === R08_KEYWORD
      }, { timeout: 20000 })
      await input.fill(R08_KEYWORD)
      const response = await responsePromise
      const elapsed = Date.now() - started
      const json = await response.json()
      const total = Number(json?.data?.total)
      assert(elapsed >= 200, `keyword filter fired automatically after the debounce (elapsed=${elapsed}ms, no manual click)`)
      assert(elapsed < 8000, `keyword filter response arrived promptly (elapsed=${elapsed}ms)`)
      await page.waitForTimeout(700)
      const tableText = await page.locator('.table-wrap').innerText()
      assert(tableText.includes(R08_KEYWORD), `table shows the matching article ${R08_KEYWORD}`)
      if (R08_POST_ID) assert(tableText.includes(R08_KEYWORD), `fixture ${R08_POST_ID} is visible under the keyword filter`)
      const totalText = await page.locator('.pagination-total').innerText()
      assert(totalText.includes(`共 ${total} 条`), `keyword filter total is ${total}: ${totalText}`)
      const searchButtons = await page.locator('.filter-bar button', { hasText: '搜索' }).count()
      assert(searchButtons === 0, `no manual search button is required (found ${searchButtons})`)
      await shot(page, 'keyword-autofilter')
      return `q=${R08_KEYWORD}; elapsed=${elapsed}ms; total=${total}`
    }, { token })
  },

  'AC-R08-03': async () => {
    if (!R08_KEYWORD || !R08_DRAFT_ID || !R08_MARKER) throw new Error('FA7 R08 preview env is not set')
    const token = await getAdminToken()
    await withPage(async (page) => {
      await openAdminPosts(page, token)
      const responsePromise = page.waitForResponse((r) => {
        if (r.request().method() !== 'GET' || !r.url().includes('/api/v1/admin/posts')) return false
        return new URL(r.url()).searchParams.get('q') === R08_KEYWORD
      }, { timeout: 20000 })
      await page.locator('.filter-bar input.input').fill(R08_KEYWORD)
      await responsePromise
      await page.waitForTimeout(700)
      const row = page.locator('.table-wrap tbody tr', { hasText: R08_KEYWORD }).first()
      await row.waitFor({ timeout: 15000 })
      const previewLink = row.locator('a', { hasText: '预览' }).first()
      assert(await previewLink.count() === 1, 'the admin row offers a 预览 action')
      await previewLink.click()
      await page.waitForURL((url) => url.pathname === `/posts/${R08_DRAFT_ID}` && url.searchParams.get('preview') === '1', { timeout: 30000 })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(700)
      const title = await page.locator('.article-head h1').innerText()
      const content = await page.locator('.article-content').innerText()
      assert(title.includes(R08_KEYWORD), `preview page shows the draft title: ${title}`)
      assert(content.includes(R08_MARKER), 'preview page shows the unpublished draft body')
      await shot(page, 'preview-draft')
      return `preview url=${new URL(page.url()).pathname}${new URL(page.url()).search}; title=${title}`
    }, { token })
  },

  'AC-R08-04': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => location.pathname === '/' && new URLSearchParams(location.search).get('forbidden') === '1', null, { timeout: 20000 })
      await page.waitForTimeout(500)
      const bodyText = await page.locator('body').innerText()
      assert(await page.locator('.admin-shell').count() === 0, 'the admin shell is not rendered for a non-admin user')
      assert(!bodyText.includes('文章管理') && !bodyText.includes('墨栈 Admin'), 'non-admin cannot see admin article data')
      await shot(page, 'author-admin-forbidden')
      return `author redirected to ${new URL(page.url()).pathname}${new URL(page.url()).search}`
    }, { token: AUTHOR_TOKEN })
  },

  'AC-R09-01': async () => {
    if (!R09_TAG_A_SLUG || !R09_TAG_B_SLUG || !R09_TAG_A_NAME || !R09_TAG_B_NAME) throw new Error('FA7 R09 tag env is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.filter-chip-tag', { timeout: 20000 })
      await page.waitForTimeout(500)
      await page.locator('.filter-chip-tag', { hasText: R09_TAG_A_NAME }).first().click()
      await page.waitForFunction((slug) => {
        const raw = new URLSearchParams(location.search).get('tags') || ''
        return raw.split(',').includes(slug)
      }, R09_TAG_A_SLUG, { timeout: 15000 })
      await page.locator('.filter-chip-tag', { hasText: R09_TAG_B_NAME }).first().click()
      await page.waitForFunction(([a, b]) => {
        const raw = (new URLSearchParams(location.search).get('tags') || '').split(',')
        return raw.includes(a) && raw.includes(b)
      }, [R09_TAG_A_SLUG, R09_TAG_B_SLUG], { timeout: 15000 })
      const expected = R09_POST_IDS.length || 2
      await page.waitForFunction((count) => document.querySelectorAll('.post-list a[href^="/posts/"]').length === count, expected, { timeout: 20000 })
      await page.waitForTimeout(500)
      const hrefs = await page.locator('.post-list a[href^="/posts/"]').evaluateAll((links) => links.map((link) => link.getAttribute('href') || ''))
      const unique = [...new Set(hrefs)]
      const active = await page.locator('.filter-chip-tag.active').allTextContents()
      assert(hrefs.length === expected && unique.length === expected, `multi-tag union renders ${expected} distinct cards (got ${hrefs.length}/${unique.length})`)
      for (const id of R09_POST_IDS) assert(hrefs.some((href) => href.includes(id)), `union contains fixture ${id}`)
      assert(active.some((text) => text.includes(R09_TAG_A_NAME)) && active.some((text) => text.includes(R09_TAG_B_NAME)), `both tag chips are selected: ${active.join(',')}`)
      await shot(page, 'mobile-tag-union')
      return `tags=${new URL(page.url()).search}; cards=${hrefs.length}; active=${active.join(',')}`
    }, { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3 })
  },

  'AC-R09-02': async () => {
    if (!R09_TAG_C_SLUG || !R09_TAG_D_SLUG || R09_ALL_TOTAL < 0) throw new Error('FA7 R09 clear-filter env is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?tag=${encodeURIComponent(R09_TAG_C_SLUG)}&tags=${encodeURIComponent(R09_TAG_D_SLUG)}`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => document.querySelectorAll('.filter-chip-tag.active').length === 2, null, { timeout: 20000 })
      await page.waitForTimeout(500)
      const clear = page.locator('button', { hasText: '清除筛选' })
      assert(await clear.count() === 1, '清除筛选 is shown when tags are selected')
      await clear.click()
      await page.waitForFunction(() => {
        const params = new URLSearchParams(location.search)
        return !params.has('tag') && !params.has('tags') && !params.has('q') && !params.has('category')
      }, null, { timeout: 20000 })
      const expectedCards = Math.min(5, R09_ALL_TOTAL)
      await page.waitForFunction((count) => document.querySelectorAll('.post-list a[href^="/posts/"]').length === count, expectedCards, { timeout: 20000 })
      await page.waitForTimeout(500)
      const active = await page.locator('.filter-chip-tag.active').count()
      const hrefs = await page.locator('.post-list a[href^="/posts/"]').evaluateAll((links) => links.map((link) => link.getAttribute('href') || ''))
      const totalText = await page.locator('.pager-total').innerText()
      assert(active === 0, `no tag chip remains selected after clearing (got ${active})`)
      assert(hrefs.length === expectedCards && [...new Set(hrefs)].length === expectedCards, `cleared list shows page 1 of all public articles (${hrefs.length}/${expectedCards})`)
      assert(totalText.includes(`共 ${R09_ALL_TOTAL} 篇`), `cleared list total is ${R09_ALL_TOTAL}: ${totalText}`)
      await shot(page, 'mobile-filters-cleared')
      return `cleared url=${new URL(page.url()).pathname}${new URL(page.url()).search}; cards=${hrefs.length}; total=${totalText.trim()}`
    }, { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3 })
  },

  'AC-R09-03': async () => {
    if (!R09_DUP_PATH || !R09_POST_IDS.length) throw new Error('FA7 R09 duplicate env is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}${R09_DUP_PATH}`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction((count) => document.querySelectorAll('.post-list article.post-card').length === count, R09_EXPECTED, { timeout: 20000 })
      await page.waitForTimeout(600)
      const cardCount = await page.locator('.post-list article.post-card').count()
      const hrefs = await page.locator('.post-list article.post-card a[href^="/posts/"]').evaluateAll((links) => links.map((link) => link.getAttribute('href') || ''))
      const unique = [...new Set(hrefs)]
      const totalText = await page.locator('.pager-total').innerText()
      assert(cardCount === R09_EXPECTED && unique.length === R09_EXPECTED, `duplicate tag conditions render ${R09_EXPECTED} unique cards (cards=${cardCount}, uniqueLinks=${unique.length})`)
      for (const id of R09_POST_IDS) assert(hrefs.some((href) => href.includes(id)), `deduped result contains fixture ${id}`)
      assert(totalText.includes(`共 ${R09_EXPECTED} 篇`), `deduped total is ${R09_EXPECTED}: ${totalText}`)
      await shot(page, 'duplicate-tag-dedup')
      return `url=${new URL(page.url()).search}; cards=${cardCount}; unique=${unique.length}`
    })
  },

  'AC-R10-01': async () => {
    let editorEvidence = ''
    await withPage(async (page) => {
      await openEditor(page)
      const statusSelect = page.locator('.settings-drawer label', { hasText: '状态' }).locator('select').first()
      const options = await statusSelect.locator('option').allTextContents()
      assert(options.length === 3 && options.includes('草稿') && options.includes('已发布') && options.includes('已下架'), `editor status options are draft/published/offline only: ${options.join(',')}`)
      assert(!options.some((text) => /定时|预约|排期|scheduled/i.test(text)), 'editor has no scheduled-publish status option')
      assert(await page.locator('input[type=datetime-local], input[type=date]').count() === 0, 'editor has no date/time scheduling control')
      const drawerText = await page.locator('.settings-drawer').innerText()
      assert(!/定时发布|预约发布|发布时间|发布日历|排期/.test(drawerText), 'article settings have no scheduled-publish control or text')
      editorEvidence = options.join(',')
      await shot(page, 'editor-no-schedule')
    }, { token: AUTHOR_TOKEN })
    let sidebar = ''
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.admin-shell', { timeout: 30000 })
      sidebar = await page.locator('.admin-sidebar').innerText()
      assert(!/发布日历|排期|定时发布|预约发布/.test(sidebar), `admin sidebar has no publish-calendar entry: ${sidebar.replace(/\s+/g, ' ').trim()}`)
      await shot(page, 'admin-no-calendar')
    }, { token: await getAdminToken() })
    return `editor statuses=${editorEvidence}; admin sidebar=${sidebar.replace(/\s+/g, ' ').trim()}`
  },

  'AC-R10-02': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.admin-shell', { timeout: 30000 })
      await page.waitForTimeout(700)
      const sidebar = await page.locator('.admin-sidebar').innerText()
      const main = await page.locator('.admin-main').innerText()
      assert(!/发布日历|排期|定时发布|预约发布/.test(sidebar), 'admin sidebar has no calendar or schedule entry')
      assert(!/发布日历|排期|定时发布|预约发布/.test(main), 'admin main area has no schedule/calendar data')
      assert(await page.locator('input[type=date], input[type=datetime-local], .calendar, .schedule-table').count() === 0, 'admin page has no date picker or calendar widget')
      await shot(page, 'admin-no-schedule-data')
      return `admin sidebar=${sidebar.replace(/\s+/g, ' ').trim()}`
    }, { token: await getAdminToken() })
  },

  'AC-R10-03': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/admin`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.admin-shell', { timeout: 30000 })
      await page.waitForTimeout(700)
      const sidebar = await page.locator('.admin-sidebar').innerText()
      const main = await page.locator('.admin-main').innerText()
      assert(!/发布日历|排期|日历/.test(sidebar), 'admin sidebar has no publish-calendar entry')
      assert(await page.locator('.calendar, .publish-calendar, table.calendar, [data-calendar]').count() === 0, 'admin page renders no calendar widget')
      assert(!/取消预约|取消定时/.test(main), 'admin article area has no cancel-schedule action')
      await shot(page, 'admin-no-publish-calendar')
      return `admin sidebar=${sidebar.replace(/\s+/g, ' ').trim()}`
    }, { token: await getAdminToken() })
    await withPage(async (page) => {
      await openEditor(page)
      const pageText = await page.locator('.editor-page').innerText()
      assert(!/取消预约|定时发布|预约发布|发布时间/.test(pageText), 'editor has no scheduled-publish or cancel-schedule control')
      assert(await page.locator('input[type=datetime-local], input[type=date]').count() === 0, 'editor has no date/time scheduling input')
      await shot(page, 'editor-no-cancel-schedule')
    }, { token: AUTHOR_TOKEN })
    return 'no publish calendar in admin and no schedule/cancel control in editor'
  },

  'AC-R11-01': async () => {
    if (!R11_POST_ID) throw new Error('FA7_R11_POST_ID is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R11_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(700)
      const likeButtons = await page.locator('button', { hasText: /点赞|喜欢/ }).count()
      const likeElements = await page.locator('.like-btn, .like-button, [data-action="like"], [data-testid="like"]').count()
      assert(likeButtons === 0 && likeElements === 0, `article page has no like control (buttons=${likeButtons}, elements=${likeElements})`)
      const bodyText = await page.locator('.article-page').innerText()
      assert(!/点赞数|点赞/.test(bodyText), 'article page shows no like count or like action text')
      await shot(page, 'no-like-control')
      return `like buttons=${likeButtons}; like elements=${likeElements}`
    })
  },

  'AC-R11-02': async () => {
    if (!R11_POST_ID) throw new Error('FA7_R11_POST_ID is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R11_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(600)
      const favoriteButtons = await page.locator('button', { hasText: /收藏/ }).count()
      const favoriteElements = await page.locator('.favorite-btn, [data-action="favorite"], [data-testid="favorite"]').count()
      assert(favoriteButtons === 0 && favoriteElements === 0, `article page has no favorite control (buttons=${favoriteButtons}, elements=${favoriteElements})`)
      await shot(page, 'article-no-favorite')
    }, { token: AUTHOR_TOKEN })
    await withPage(async (page) => {
      await page.goto(`${WEB}/account`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.account-page', { timeout: 30000 })
      await page.waitForTimeout(600)
      const text = await page.locator('.account-page').innerText()
      assert(!/我的收藏|收藏/.test(text), 'account page has no 我的收藏 entry')
      assert(await page.locator('.favorites, .favorite-list, [data-section="favorites"]').count() === 0, 'account page renders no favorites section')
      await shot(page, 'account-no-favorites')
    }, { token: AUTHOR_TOKEN })
    return 'no favorite control on article and no 我的收藏 section on account'
  },

  'AC-R11-03': async () => {
    if (!R11_POST_ID) throw new Error('FA7_R11_POST_ID is not set')
    await withPage(async (page) => {
      await page.goto(`${WEB}/posts/${R11_POST_ID}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight * 0.55))
      await page.waitForTimeout(800)
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.article-page', { timeout: 30000 })
      await page.waitForTimeout(900)
      const text = await page.locator('body').innerText()
      assert(!/继续阅读|上次阅读|阅读进度|继续上次|恢复阅读/.test(text), 'reopening the article shows no resume-reading prompt')
      assert(await page.locator('.resume-reading, .reading-resume, [data-action="resume"]').count() === 0, 'reopening the article renders no resume widget')
      const storageKeys = await page.evaluate(() => Object.keys(localStorage).filter((key) => /progress|reading|resume/i.test(key)))
      assert(storageKeys.length === 0, `localStorage stores no reading progress (${storageKeys.join(',')})`)
      await shot(page, 'no-resume-prompt')
      return 'no resume prompt/widget/progress key after reopen'
    })
  },

  'AC-R12-01': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.search-box input.input', { timeout: 20000 })
      const suggestRequests = []
      page.on('request', (request) => {
        if (/suggest/i.test(request.url())) suggestRequests.push(request.url())
      })
      const input = page.locator('.search-box input.input')
      await input.fill('部')
      await page.waitForTimeout(700)
      const suggestionSelectors = await page.locator('.search-suggest, .suggestions, .suggestion-list, .autocomplete, [role="listbox"], [data-suggest]').count()
      assert(suggestionSelectors === 0, `typing 部 renders no suggestion dropdown (elements=${suggestionSelectors})`)
      assert(suggestRequests.length === 0, `typing 部 issues no suggestion request (${suggestRequests.join(',')})`)
      assert((await input.inputValue()) === '部', 'the search box keeps the typed 部')
      await shot(page, 'no-search-suggestions')
      return `suggestion elements=${suggestionSelectors}; suggest requests=${suggestRequests.length}`
    })
  },

  'AC-R12-02': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog?q=Vue`, { waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => {
        const list = document.querySelector('.post-list')
        if (!list) return true
        return !(list.textContent || '').includes('正在加载文章')
      }, null, { timeout: 20000 })
      await page.waitForTimeout(1000)
      const highlightElements = await page.locator('mark, .highlight, .search-highlight, .keyword-highlight, [data-highlight]').count()
      const listHtml = await page.evaluate(() => document.querySelector('.post-list')?.innerHTML || '')
      assert(highlightElements === 0, `search results render no highlight elements (got ${highlightElements})`)
      assert(!/<mark[\s>]/i.test(listHtml), 'search result markup contains no <mark> highlight')
      await shot(page, 'no-search-highlight')
      return `q=Vue; highlight elements=${highlightElements}`
    })
  },

  'AC-R12-03': async () => {
    await withPage(async (page) => {
      await page.goto(`${WEB}/blog`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.search-box input.input', { timeout: 20000 })
      await submitBlogSearch(page, 'Vue')
      await submitBlogSearch(page, 'Go')
      const input = page.locator('.search-box input.input')
      await input.click()
      await page.waitForTimeout(500)
      const text = await page.locator('body').innerText()
      assert(!/搜索历史|最近搜索|历史记录/.test(text), 'search box shows no search-history section')
      assert(await page.locator('.search-history, .history-list, .recent-searches, [data-search-history]').count() === 0, 'no search-history widget is rendered')
      const storageKeys = await page.evaluate(() => Object.keys(localStorage).filter((key) => /search.*histor|histor.*search/i.test(key)))
      assert(storageKeys.length === 0, `localStorage stores no search history (${storageKeys.join(',')})`)
      await shot(page, 'no-search-history')
      return `searches=2; history elements=0; history keys=${storageKeys.length}`
    })
  },
}

let browser = null
try {
  const { chromium } = requireFromExecutor('playwright')
  browser = await chromium.launch({ headless: true, channel: 'chrome' }).catch(async (error) => {
    logError('chrome channel unavailable, falling back to msedge:', String(error.message).split('\n')[0])
    return chromium.launch({ headless: true, channel: 'msedge' })
  })
  const branch = BRANCHES[acId]
  if (!branch) {
    console.log(JSON.stringify({ acId, ok: false, fatal: true, observed: `unsupported acId ${acId}`, assertions: [], screenshot: null, screenshots: [] }))
    process.exit(2)
  }
  const observed = await branch()
  console.log(JSON.stringify({
    acId,
    ok: assertions.length > 0 && assertions.every((item) => item.ok),
    observed: String(observed || ''),
    assertions,
    screenshot: screenshots[0] || null,
    screenshots,
  }))
  await browser.close()
} catch (error) {
  logError(error?.stack || String(error))
  try { if (browser) await browser.close() } catch { }
  console.log(JSON.stringify({
    acId,
    ok: false,
    fatal: true,
    observed: `ui-check fatal: ${error?.message || String(error)}`,
    assertions: [{ kind: 'ui', ok: false, detail: `ui-check fatal: ${error?.message || String(error)}` }],
    screenshot: screenshots[0] || null,
    screenshots,
  }))
  process.exit(1)
}
