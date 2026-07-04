// Package httpx holds shared HTTP building blocks reused by every module:
// the middleware chain (request-id → recover → logging → metrics, P5), JSON
// request/response helpers, and error rendering. Depends on no internal module.
package httpx
