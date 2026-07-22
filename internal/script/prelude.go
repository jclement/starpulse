package script

// The prelude is a small standard library, written in Lua and loaded before
// every script. It adds no capability the sandbox did not already have — it
// only wraps request, store and write into the patterns scripts reach for,
// so a page does not paste the same identity check and key/value plumbing
// each time. It runs as its own chunk so a user script keeps its own line
// numbers in error messages.
const prelude = `
sp = {}

-- __tostr backs <?= expr ?>: nil prints as nothing, not "nil".
function __tostr(v)
  if v == nil then return "" end
  return tostring(v)
end

-- stop() ends the script early and cleanly. It raises a sentinel the engine
-- recognises as a normal finish, not a failure, so whatever was written so
-- far is what the reader gets.
function sp.stop()
  error("__sp_stop__", 0)
end

-- identity() is the soft form: the caller's id, or "" when anonymous.
function sp.identity()
  return request.identity
end

-- verified() reports whether the identity is cryptographically bound — a
-- gemini client certificate or an ssh key — rather than a bearer cookie a
-- browser hands over. A page that matters should not trust a cookie the way
-- it trusts a key.
function sp.verified()
  return request.identity_verified
end

-- require_identity() is the hard form: return the id, or write a standard
-- "who are you" message appropriate to the door and stop. This is the
-- boilerplate every gated page would otherwise carry.
function sp.require_identity(message)
  if request.identity ~= "" then
    return request.identity
  end
  write(message or "This page needs to know who you are.")
  write("\n\n")
  if request.proto == "gemini" then
    write("Set up a client certificate for this capsule, then reload.\n")
  elseif request.proto == "telnet" then
    write("Telnet has no identity — visit over the web or gemini instead.\n")
  else
    write("Your session was not recognised — allow cookies and reload.\n")
  end
  sp.stop()
end

-- require_strong() is require_identity that also insists the identity be
-- verified: a cookie or a bare session will not do, only a key or a
-- certificate.
function sp.require_strong(message)
  if request.identity ~= "" and request.identity_verified then
    return request.identity
  end
  write(message or "This page needs a verified identity, not just a browser session.")
  write("\n\n")
  if request.proto == "gemini" then
    write("Present a client certificate for this capsule, then reload.\n")
  elseif request.proto == "ssh" or request.proto == "telnet" then
    write("Connect over SSH with a public key.\n")
  else
    write("A browser session is not verified — use gemini with a client certificate, or ssh with a key.\n")
  end
  sp.stop()
end

-- Key/value helpers over the per-script store.

function sp.get(key, default)
  local v = store.get(key)
  if v == nil then return default end
  return v
end

function sp.set(key, value)
  store.set(key, value)
end

function sp.del(key)
  store.delete(key)
end

-- num/inc treat a value as a counter.
function sp.num(key, default)
  return tonumber(store.get(key)) or default or 0
end

function sp.inc(key, by)
  local n = (tonumber(store.get(key)) or 0) + (by or 1)
  store.set(key, tostring(n))
  return n
end

-- list/push treat a value as newline-separated lines — a log, a set of
-- guesses, a guestbook.
function sp.list(key)
  local t = {}
  local raw = store.get(key)
  if raw then
    for line in raw:gmatch("[^\n]+") do t[#t + 1] = line end
  end
  return t
end

function sp.push(key, value)
  store.set(key, (store.get(key) or "") .. value .. "\n")
end

-- has(list, value) — a linear membership test, for "did I already…".
function sp.has(list, value)
  for _, v in ipairs(list) do
    if v == value then return true end
  end
  return false
end
`

// spStop is the sentinel sp.stop() raises; the engine treats it as a clean
// early finish rather than a script error.
const spStop = "__sp_stop__"
