package main

import "regexp"

var credRe = regexp.MustCompile(`(rest:https?://)[^:@/]+:[^@/]+@`)

// Redact masks user:pass in rest: URLs before logging or serving.
func Redact(s string) string { return credRe.ReplaceAllString(s, "${1}***:***@") }
