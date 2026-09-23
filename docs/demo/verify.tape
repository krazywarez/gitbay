# Part two: the code signup.tape's register mailed, as DEMO_CODE. A code
# works once, so a rerun needs a fresh account.
Output docs/demo/out/verify/
Source docs/demo/common.tape

Hide
Type "code=$DEMO_CODE && clear" Enter
Sleep 500ms
Show
Type "# the code from the mail" Enter
Sleep 600ms
Type "ssh git@gitbay.org email verify $code" Enter
Sleep 2s
