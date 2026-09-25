import {
  appendResponseHeader,
  createError,
  defineEventHandler,
  getHeaders,
  getMethod,
  getQuery,
  getRouterParam,
  readRawBody,
  setResponseHeader,
  setResponseStatus,
} from 'h3'

/**
 * 同源 API proxy：瀏覽器只跟 Nuxt 說話，Go API 不對外曝露。
 *
 * 轉發策略是「預設全部轉發，明確排除少數」而非逐項列舉要轉發的項目。
 * 先前的 allowlist 做法造成過五次靜默失效（cookie、CSRF token、Retry-After、
 * X-Forwarded-For、multipart），每次都是後端新增了有語意的 header 或內容型別，
 * 但 proxy 不知道要轉發它。排除清單讓新增的 header 預設就能通過。
 */

/**
 * 不可轉發到後端的請求 header。
 *
 * hop-by-hop header 描述的是「這一段連線」的語意，轉發到下一跳會造成錯亂；
 * host 與 content-length 由 fetch 依實際請求重新計算。
 */
const REQUEST_HEADER_BLOCKLIST = new Set([
  // RFC 7230 定義的 hop-by-hop header
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  // 指向 Nuxt 而非後端，轉發會讓後端誤判自己的位址
  'host',
  // 由 fetch 依實際 body 重新計算；沿用舊值會造成長度不符
  'content-length',
  // 我們自行組出 XFF 鏈（見下方），不直接沿用客戶端提供的值
  'x-forwarded-for',
])

/**
 * 不可回傳給瀏覽器的回應 header。
 *
 * set-cookie 另行處理（需要逐筆 append，不能合併成單一值）；
 * content-encoding 與 content-length 在 $fetch 解壓後已不符實際內容。
 */
const RESPONSE_HEADER_BLOCKLIST = new Set([
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  // 由 appendResponseHeader 逐筆處理
  'set-cookie',
  // $fetch 已解壓內容，沿用這兩個值會讓瀏覽器解析失敗
  'content-encoding',
  'content-length',
])

/**
 * 需要以原始位元組處理的回應內容型別。
 *
 * 依回應實際回報的 content-type 判斷，不看請求路徑——
 * 先前以 path.includes('/download') 判斷，任何新的二進位端點都會踩雷。
 */
const TEXT_CONTENT_PATTERN = /^(?:text\/|application\/(?:json|xml|javascript|x-www-form-urlencoded)|[^;]*\+json)/i

function isTextualContentType(contentType: string): boolean {
  if (!contentType) return true
  return TEXT_CONTENT_PATTERN.test(contentType.trim())
}

export default defineEventHandler(async (event): Promise<unknown> => {
  const path = getRouterParam(event, 'path')
  if (!path) {
    throw createError({ statusCode: 404, statusMessage: 'API path is required' })
  }

  const config = useRuntimeConfig()
  const baseUrl = String(config.backendInternalUrl).replace(/\/$/, '')
  const method = getMethod(event)
  const incoming = getHeaders(event)

  const headers: Record<string, string> = {}
  for (const [name, value] of Object.entries(incoming)) {
    if (value === undefined) continue
    if (REQUEST_HEADER_BLOCKLIST.has(name.toLowerCase())) continue
    headers[name] = value
  }

  // 轉發真實來源 IP，否則後端的限流會把 proxy 後面的所有使用者
  // 視為同一個來源，共用一份配額。
  //
  // 後端取 X-Forwarded-For 的「最後一跳」，因此把我們觀察到的對端位址
  // 附加在既有鏈的尾端；客戶端自行偽造的前綴會被忽略。
  const observedIP = event.node.req.socket.remoteAddress ?? ''
  const forwardedChain = incoming['x-forwarded-for']
  const chain = [forwardedChain, observedIP].filter(Boolean).join(', ')
  if (chain) {
    headers['x-forwarded-for'] = chain
  }

  const query = getQuery(event)
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (Array.isArray(value)) {
      value.forEach((entry) => search.append(key, String(entry)))
    } else if (value !== undefined && value !== null) {
      search.append(key, String(value))
    }
  }
  const queryString = search.toString()

  const options: Record<string, unknown> = {
    method,
    headers,
    // 自行處理狀態碼，讓後端的 401/403/422 原樣傳回前端而非被包成 500
    ignoreResponseError: true,
    // 一律取原始位元組；是否需要解析由回應的 content-type 決定
    responseType: 'arrayBuffer',
  }

  if (!['GET', 'HEAD'].includes(method)) {
    // 一律原樣轉發請求主體：不解析就不會破壞任何內容型別
    // （JSON 與 multipart 都只是位元組，後端才是該解析它的一方）
    const body = await readRawBody(event, false)
    if (body !== undefined && body !== null) {
      options.body = body
    }
  }

  const url = `${baseUrl}/${path}${queryString ? `?${queryString}` : ''}`
  const response = await $fetch.raw(url, options)

  for (const cookie of response.headers.getSetCookie?.() ?? []) {
    appendResponseHeader(event, 'set-cookie', cookie)
  }

  for (const [name, value] of response.headers.entries()) {
    if (RESPONSE_HEADER_BLOCKLIST.has(name.toLowerCase())) continue
    setResponseHeader(event, name, value)
  }

  setResponseStatus(event, response.status)

  const payload = response._data
  if (!(payload instanceof ArrayBuffer)) {
    return payload
  }

  const buffer = Buffer.from(payload)
  const contentType = response.headers.get('content-type') ?? ''

  // 文字型內容轉回字串，讓 $fetch 的呼叫端照常取得解析後的 JSON；
  // 其餘（PDF、圖片等）以原始位元組回傳。
  if (isTextualContentType(contentType)) {
    return buffer.toString('utf8')
  }
  return buffer
})
