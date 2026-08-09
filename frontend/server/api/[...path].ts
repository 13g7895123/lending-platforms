import {
  createError,
  defineEventHandler,
  getHeaders,
  getMethod,
  getRouterParam,
  readBody,
} from 'h3'

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

  for (const name of ['accept', 'authorization', 'content-type', 'x-request-id']) {
    const value = incoming[name]
    if (value) headers[name] = value
  }

  const options: Record<string, unknown> = {
    method,
    headers,
  }

  if (!['GET', 'HEAD'].includes(method)) {
    options.body = await readBody(event)
  }

  return await $fetch(`${baseUrl}/${path}`, options)
})
