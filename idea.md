Ok.  Review ~/Developer/owg-capsule

We're using some of the code / concepts but we're making it more of a smolweb CMS

"starpulse"

github.com/jclement/starpulse
github action to build
docker container GHCR
+ golang binaries for all the usaul platforms

Idea is it's a single binary GoLang app (CLANG=0)

config dir
 (/etc/starpulse, ~/.config/starpulse)
   - config.yaml

data dir
  - starpulse.sqlite
  - certs (for lets encrypt certs)
  - tor stuff

Toggles via config or env for

admin_password (used for editing content for HTTP or SSH)
  - plaintext or hashed (some bcrypt or something)
certificates (user for editing content via TITAN)
services to enable and ports (GEMINI, HTTP, HTTPS, future TELNET, SSH)
hostname (for lets encrypt)
enable_tor (requires local tor binary) - registers hidden service automagically, runs own tor instance


/mcp for MCP tooling to do all the stuff.  Bearer token = password.
/api rest type APIs for updating content (bearer token = password)

Web pages shouldn't use/need much JS?  (Maybe we don't need any? ideal)

should try and shed root if it can

CLI

install - installs binary to /opt/starpulse/bin, creates sample config in /etc/starpulse (and data in /var/???).  Register with systemd
uninstall - undoes that?  maybe leaving data and config?  prompts?
self-update - install update from github
serve - run server (colorful logging)

author content in gemtext (either for web UI - login page with admin/{password}) or GEMINI/TITAN (via. client certs).
content stores in sqlite database
per page stats by request type (GEMINI, HTTP, or GEMINI+TOR, HTTP+TOR)

Simple web UI
server side includes type thing ({{count}}, {{list [folder] [limit]}}), ...
.header and .footer files are included in pages and inherited down

Setup server root@owg.fyi with this.  Maybe kill the current docker thing and install with systemd?  or docker. Whatever you think?

Tonnes of good tests.

Would it be crazy to have a few other special files like .header and .footer?  like .theme (which is CSS which is applied to web UI on that folder down?)
When logged in as admin, pages have a subtle edit link somewhere that opens full page editor.

search page with sqlite full text seach is a must
if you can bootstrap this server with the content from the previous verison of that, that'd be great.

Perhaps we store page versions to allow undo?
Allow uploading binaries like images, etc?  Store in DB?  Reasonable max size?

Maybe some mechanism for now posts.  From web or titan.  {{now}} to include latest in a page?

This is going to be the best smol web hosting platform for a single use ever.  Again you own root@owg.fyi.  Get this up and running and deployed.  And awesome.  Perhaps modifying content by the API you add?!