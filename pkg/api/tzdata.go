package api

// The ?tz= filter parameter (parseZone) loads IANA time zones. The server
// image (Alpine) ships no zoneinfo, so the database is embedded (~450 KB).
import _ "time/tzdata"
