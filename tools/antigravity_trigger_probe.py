#!/usr/bin/env python3
"""Antigravity false-429 trigger probe (read-only diagnostic).

Sends management api-call requests on ONE chosen credential to classify whether
a given systemInstruction phrase draws the Antigravity instant false-429, binary
-searches the minimal triggering substring, and verifies that inserting one
U+034F (combining grapheme joiner, invisible, meaning-preserving) flips it to 200.

Read-only: it never changes credential state, egress, config, or the pool. It
only issues POST /v8/management/requests/api-call through a management helper.

Usage:
  antigravity_trigger_probe.py --mgmt ./mgmt.sh --auth <AUTH_INDEX> \
      --project aicode-consumers [--model gemini-2.5-flash-lite] \
      [--reps 2] --phrase "Global chat rules (higher priority than character instructions):"
  antigravity_trigger_probe.py ... --phrase-file prompt.txt            # read phrase from file
  antigravity_trigger_probe.py ... --no-bisect                         # just classify + CGJ check

mgmt helper contract: `<mgmt> POST /requests/api-call <json>` prints a JSON body
with a top-level "status_code".
"""
import argparse, json, subprocess, sys, time, uuid, random

CGJ = "\u034f"

def call(mgmt, auth, project, model, text):
    req = {"sessionId": f"-{random.randint(10**8,10**9)}",
           "contents": [{"role": "user", "parts": [{"text": "Say OK."}]}],
           "generationConfig": {"maxOutputTokens": 16}}
    if text is not None:
        req["systemInstruction"] = {"role": "user", "parts": [{"text": text}]}
    inner = {"model": model, "userAgent": "antigravity", "requestType": "agent",
             "project": project, "requestId": f"agent-{uuid.uuid4()}", "request": req}
    body = {"auth_index": auth, "method": "POST",
            "url": "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse",
            "header": {"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json",
                       "User-Agent": "antigravity/hub/2.9.1 darwin/arm64"},
            "data": json.dumps(inner, ensure_ascii=False)}
    r = subprocess.run([mgmt, "POST", "/requests/api-call", json.dumps(body, ensure_ascii=False)],
                       capture_output=True, text=True)
    try:
        return json.loads(r.stdout).get("status_code")
    except Exception:
        return "ERR"

def verdict(mgmt, auth, project, model, text, reps):
    codes = []
    for _ in range(reps):
        codes.append(call(mgmt, auth, project, model, text))
        time.sleep(2.2)
    if all(c == 429 for c in codes):
        return "429", codes
    if all(c == 200 for c in codes):
        return "200", codes
    return "MIX", codes

def cgj_first(s):
    for i, _ in enumerate(s):
        if i == 0:
            continue
        return s[:i] + CGJ + s[i:]
    return s

def bisect_min(mgmt, auth, project, model, text, reps):
    """Shrink text to a minimal contiguous substring that still 429s."""
    cur = text
    changed = True
    while changed and len(cur) > 1:
        changed = False
        for frac in (0.5, 0.75):
            cut = max(1, int(len(cur) * frac))
            for cand in (cur[:cut], cur[-cut:]):
                v, _ = verdict(mgmt, auth, project, model, cand, reps)
                if v == "429":
                    cur = cand
                    changed = True
                    break
            if changed:
                break
    return cur

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mgmt", required=True)
    ap.add_argument("--auth", required=True)
    ap.add_argument("--project", default="aicode-consumers")
    ap.add_argument("--model", default="gemini-2.5-flash-lite")
    ap.add_argument("--reps", type=int, default=2)
    ap.add_argument("--phrase")
    ap.add_argument("--phrase-file")
    ap.add_argument("--no-bisect", action="store_true")
    a = ap.parse_args()
    phrase = a.phrase
    if a.phrase_file:
        phrase = open(a.phrase_file, encoding="utf-8").read()
    if not phrase:
        ap.error("provide --phrase or --phrase-file")

    v, codes = verdict(a.mgmt, a.auth, a.project, a.model, phrase, a.reps)
    print(f"[original] {v} {codes}  len={len(phrase)}")
    if v != "429":
        print("not a trigger on this credential/model; nothing to minimize.")
        return
    minimal = phrase
    if not a.no_bisect:
        minimal = bisect_min(a.mgmt, a.auth, a.project, a.model, phrase, a.reps)
        mv, mc = verdict(a.mgmt, a.auth, a.project, a.model, minimal, a.reps)
        print(f"[minimal ] {mv} {mc}  len={len(minimal)}  :: {minimal[:80]!r}")
    fix = cgj_first(minimal)
    fv, fc = verdict(a.mgmt, a.auth, a.project, a.model, fix, a.reps)
    print(f"[+U+034F ] {fv} {fc}  (one CGJ after first rune; meaning-preserving)")
    if fv == "200":
        print("=> add rule: match=<minimal>, obfuscate:true (prompt-rewrites-extra)")
    else:
        print("=> one CGJ insufficient; trigger likely spans multiple positions (phase-2 deep-capture)")

if __name__ == "__main__":
    main()
