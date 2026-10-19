"""Regenerates internal/redact/testdata/cases.json with the Pegasus hook as oracle.

usage: python3 -I gen-cases.py <pegasus worktree> <current cases.json> <out>

The inputs of the current file are kept (their expected output is recomputed),
the cases of the Pegasus hook tests (WIDENED/SURVIVING/REDACTION/POSITIONAL) and
the extra ones below are added. Review the printed list of changed old cases:
it must be explained by the catalog change. Then copy <out> over the file.
"""
import ast, json, re, sys

wt, old_path, out_path = sys.argv[1:4]
catalog = json.load(open(f"{wt}/src/pegasus/content/security/credential-transport-catalog.json"))
src = open(f"{wt}/src/pegasus/adapters/claudecode/assets/engram-hook.py").read()
src = src.replace("__CONFIG_JSON__", repr(json.dumps({"catalog": catalog})))
ns = {"__name__": "oracle"}
exec(compile(src, "engram-hook.py", "exec"), ns)
ns["_token"] = lambda name: "[REDACTED:%s]" % name
orig = ns["_is_placeholder"]
extra = re.compile(r"^\[REDACTED:[A-Z0-9_]+\]\Z")
ns["_is_placeholder"] = lambda v: bool(extra.search(v)) or orig(v)

def oracle(text):
    return ns["redact"](ns["cut_to_detect_cap"](text))

def literal_list(path, name):
    tree = ast.parse(open(path).read())
    for node in tree.body:
        if isinstance(node, ast.Assign) and getattr(node.targets[0], "id", "") == name:
            return eval(compile(ast.Expression(node.value), path, "eval"), {})
    raise SystemExit("missing " + name)

hooks = f"{wt}/tests/test_claudecode_engram_hooks.py"
new = []
for i, (case, _) in enumerate(literal_list(hooks, "WIDENED_CASES")):
    new.append((f"v791_widened_{i:02d}", case))
for i, case in enumerate(literal_list(hooks, "SURVIVING_CASES")):
    new.append((f"v791_surviving_{i:02d}", case))
for i, case in enumerate(literal_list(hooks, "REDACTION_CASES")):
    new.append((f"v791_parity_{i:02d}", case))
for i, (case, _, _) in enumerate(literal_list(hooks, "POSITIONAL_CASES")):
    new.append((f"v791_positional_{i:02d}", case))
for i, case in enumerate([
    "<private><private>x</private></private>",
    "a <private>b <private>c</private> d</private> e",
    "<private>unclosed and </private> then <private>again",
    "keep <private>secret to the end of the line\nnext line",
    "<private>\nparagraph one\nstill one\n\nparagraph two",
    "a stray </private> close and `<private>` mentioned in prose",
    "</private><private>",
    "<private>a<private>b</private>c</private>",
    "x <private>a<private>b</private>c</private> y",
    "<private>crlf unclosed secret\r\nnext line",
    "<private>   \r\n  \r\nnot in the span",
    "<PRIVATE>Up=Cased12345</PrIvAtE> and <private>n=</private> and <private></private>",
    "`<private>`secret=abcd1234`</private>` literal",
    "<private>one</private><private>two</private><private>three",
    "<private>a=b</private> <private>a=b",
    "tail <private>",
    "😀 <private>😀 secret\nafter",
    "<private>\n\n\n</private>",
]):
    new.append((f"v791_private_{i:02d}", case))
for i, case in enumerate([
    "dbPassword=hunter2hunter2 DBPassword: abc12345 myAPIKey=abcd1234efgh userPwd='abcd1234' awsSecretAccessKey=\"wJalrXUtnFEMI\"",
    "accessToken=abcd1234 AccessToken: abcd1234 access-token=abcd1234 ACCESS_TOKEN=abcd1234 XAccessToken=abcd1234",
    "clientSecret1=abcd1234 secret2Key=abcd1234 privateKEY=abcd1234 id2Token=abcd1234",
    'password="a\\\\" b="c1234567"',
    "password='it\\'s a secret value' tail",
    '{"secret": "x\\"y\\"zzzz", "token": "unterminated\\"',
    "token: callMe(arg1, arg2) and token: obj.method( and password: passwordpassword and password: Credentials",
    "pwd=abcdefghi pwd=abcdefghij pwd=Abcdefghij pwd=ABCDEFGHIJ pwd=abc_defghij pwd=abcDefghijk",
    "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSj\n\nvisible after blank",
    "-----BEGIN PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,ABCDEF\n\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSj\nshort\nrest",
    "-----BEGIN PRIVATE KEY-----\nlonely begin line only",
    "sk-ant-" + "a" * 19 + " sk-ant-" + "b" * 20 + " sk-proj-" + "c" * 20 + " glpat-" + "d" * 19 + " AIza" + "e" * 34 + " AIza" + "f" * 35 + " npm_" + "g" * 35 + " npm_" + "h" * 36,
    "Straße café ñandú password=hunter2hunter2 日本語 token=abcd1234efgh",
    "X-Token-Signature: abc123def456\nX-Foo-Signature-256: sha256=abcdef1234567890\nx-api-key: abcdef12345",
]):
    new.append((f"v791_extra_{i:02d}", case))

old = json.load(open(old_path))
cases, seen = [], set()
changed = []
for c in old["cases"]:
    exp = oracle(c["input"])
    if exp != c["expected"]:
        changed.append(c["name"])
    cases.append({"name": c["name"], "input": c["input"], "expected": exp})
    seen.add(c["input"])
for name, text in new:
    if text in seen:
        continue
    seen.add(text)
    cases.append({"name": name, "input": text, "expected": oracle(text)})
doc = {"about": "Golden cases produced by running the Pegasus redactor (engram-hook.py cut_to_detect_cap + redact, Pegasus 7.9.1 catalog) with _token -> [REDACTED:NAME] and one extra placeholder pattern ^\\[REDACTED:[A-Z0-9_]+\\]$. All values are synthetic. Regenerate with the generator when the catalog changes.", "cases": cases}
json.dump(doc, open(out_path, "w"), indent=2, ensure_ascii=False)
open(out_path, "a").write("\n")
print(len(old["cases"]), "old,", len(cases) - len(old["cases"]), "new; old expected changed by the oracle:", changed)
