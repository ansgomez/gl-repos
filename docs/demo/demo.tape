# The README screencast, recorded with VHS (https://github.com/charmbracelet/vhs)
# against made-up repositories, a fake glab and a fake Ollama: run `make demo`.

Output docs/demo-dark.gif

Require git
Set Shell "bash"
Set FontSize 15
Set Width 1240
Set Height 480
Set Padding 18
Set Framerate 15 # a slow machine drops frames at the default 50 and plays too fast
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Set WindowBar Colorful

Hide
Type '. "$GR_DEMO/env.sh"' Enter
Sleep 1s
Show

Type "gl-repos" Sleep 400ms Enter
Sleep 2.5s

# fetch everything: api-gateway turns out to be behind
Type "a" Sleep 300ms Type "f"
Sleep 2.5s
Type "a"
Sleep 800ms

# fast-forward it
Type "p"
Sleep 2s

# billing-service has uncommitted work: details, then ask the AI
Down Sleep 500ms
Enter Sleep 2.5s
Enter Sleep 500ms
Type "i" Sleep 600ms
Type "commit the retry change" Sleep 500ms Enter
Sleep 4s
Type "y"
Sleep 2s

# web-dashboard has two commits to push (asks first)
Down@120ms 6 Sleep 500ms
Type "h" Sleep 1.5s
Type "y"
Sleep 2s

# clone a project from the GitLab profile, with autocomplete
Type "c" Sleep 1s
Type "notif" Sleep 1.2s
Enter Sleep 1.5s
Enter Sleep 3s

Type "q"
Sleep 600ms
