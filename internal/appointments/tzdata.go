package appointments

// Embed the IANA timezone database so time.LoadLocation works regardless of
// whether the host/container ships zoneinfo. Scheduling correctness depends on
// timezone data being present, so we never want to rely on the environment.
import _ "time/tzdata"
