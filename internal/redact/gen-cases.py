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
corpus = json.load(open(f"{wt}/tests/fixtures/redaction_leak_corpus.json"))
for i, case in enumerate(corpus["cases"]):
    new.append((f"v792_corpus_{i:02d}", case["text"]))
for i, case in enumerate([
    # 7.9.2: escaped quotes inside a JSON string (passes 6b and 7b).
    '{"log":"password=\\"Hunter2Hunter2\\" x"}',
    '{"log":"password=\\"ab\\\\\\"cdefgh1\\" x"}',
    '{"log":"password=\\"abcd\\\\\\\\\\" x"}',
    '{"log":"\\"token\\":\\"abcdefgh1234\\", \\"user\\":\\"bob\\""}',
    '{"log":"\\"api_key\\" :\\t\\"abcdefgh1234\\""}',
    'apiKey: "SuperSecretValue99\\"AccessToken = npm_' + "a" * 36 + '\\""',
    'x token=\\"ab\\" password=\\"\\" nothing \\"quoted words here\\" password: "ab\\"cdefgh"',
    '{"log":"export TOKEN=\\"abcd1234efgh\\" && echo \\"password: \\"zzzz9999\\"\\""}',
    '{"log":"a\\nb password=\\"line\\\\nbreak1234\\" end"}',
    'tokenpassword=\\"abcdefgh12\\" mypassword=\\"abcdefgh12\\" -password=\\"abcdefgh12\\"',
    'secret=\\"unterminated1234 and then password=\\"closed-1234\\"',
    "token=\\\"" + "a" * 60 + "\\\"" + " " + 'password=\\"' + "\\\\" * 50 + 'x\\"',
    # 7.9.2: <private> padding and tag variants.
    "<private>\nVALUE1234\n</private> then VALUE1234 alone",
    "<private> SPACED1234 </private> and SPACED1234",
    "<private>\n  NAME = VALUE5678 \n</private> then VALUE5678",
    "<private>\tNAME\t=\tVALUE5678\t</private>",
    "<private >one1234567</private> <private\n>two1234567</private\n> < /private> < private> x1234567 </ private >",
    "<PRIVATE >a</PRIVATE > <\u00a0private\u00a0>b1234567<\u00a0/\u00a0private\u00a0>",
    "\uff1cprivate\uff1ethis stays\uff1c/private\uff1e and <private>\u3000 \u3000</private> and <private></private>",
    "`<private >` mention <private\n>unclosed secret\nnext line",
    "<private>NAME=</private> <private>   </private> <private>\n\n</private> end",
    "<private>\u2003PADDED1234\u2003</private> \ufeff<private>\ufeffBOM12345\ufeff</private>",
    "< /private> < /private><private> two1234 </private >",
    "<" + " " * 40 + "private" + " " * 40 + ">x1234567</private>",
    # 7.9.2: escape borders (\xNN, octal, \a, \e) before a repeated value.
    "api_key=abcdefgh1234x then \\x41abcdefgh1234x and \\101abcdefgh1234x and \\7abcdefgh1234x and \\eabcdefgh1234x and \\aabcdefgh1234x and \\1234abcdefgh1234x and \\xZZabcdefgh1234x and \\ x abcdefgh1234x",
    # 7.9.2: headers glued by a character that is not whitespace.
    "Authorization: Bearer Passw0rd2024;x-api-key: correct.horse2battery",
    "Authorization: Bearer Passw0rd2024\x1cx-api-key: correct.horse2battery",
    "x-api-key: V1value12345;Authorization: Bearer V2value12345",
    "X-Auth-Token: tok123456789;Authorization: Basic dXNlcjpwYXNz1234;x-foo-token: zzzzzzzz1234",
    # 7.9.2: astral neighbours and rune-counted floors.
    "token=abcdefghij1\U0001f600 and abcdefghij1\U0001f600 and \U00013fcfabcdefghij1 and abcdefghij1\U0001d400 and \U0001d400abcdefghij1",
    "secret: -\U000136cf< secret: \U000136cf\U000136cf\U000136cf\U000136cf token=\U0001f600\U0001f600\U0001f600\U0001f600",
    "password=\u00e9\u00e9\u00e9\u00e9 password=\u00e9\u00e9\u00e9 \u00e9\u00e9\u00e9\u00e9",
    "token=\u00e1bcd1234 \u00e1bcd1234 and x\u00e1bcd1234 and \u00e1bcd1234\u00e9",
    # 7.9.2: call-shaped values with a callee that reads like a password.
    "password=Summer2024( password=Zx9kQ2mP7(wLr) secret=Zx9kQ2mP7wLr() token=Passw0rd() password = sha256(x) token = MD5(abc) pwd = getPassword(User1) secret = utf8( pwd = hmacSHA256( password=summer2024( password=Abc1Def_2(x)",
    "password = md5($input) token = toBase64(x) pwd = readUInt8(buf) secret = decodeBase64(s) password=Vec3(1,2) token=Sha256(abc)",
]):
    new.append((f"v792_extra_{i:02d}", case))
import random
rng = random.Random(7)
vals = ["val%05dX" % n for n in range(300)]
new.append(("v792_many_values", " ".join("password=%s" % v for v in vals) + " tail " + " ".join(vals[::3]) + " and x" + vals[0]))
rep = [("tok%03dQ7z" % n) for n in range(90)]
new.append(("v792_repeated_values", " ".join("api_key=%s" % v for v in rep) + " | " + " ".join(rep) + " | " + " ".join(rep) + " | " + " ".join(rep)))
longs = ["L" * 5 + "%03d" % n + "z" * 20 for n in range(10)]
new.append(("v792_long_values", " ".join("token=%s" % v for v in longs) + " " + "".join(longs) + " " + ".".join(longs)))
nest = ["abcdefgh", "abcdefghijkl", "abcdefghijklmnopqrstuvwxyz1"]
new.append(("v792_prefix_values", "token=abcdefg1 token=abcdefg1ijkl x abcdefg1ijklm abcdefg1.abcdefg1ijkl abcdefg1ijkl abcdefg1ijklmnopqrstuvwxyz1 token=abcdefg1ijklmnopqrstuvwxyz1"))
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
    # 7.9.1 fix round: escapes before a repeated value, long values, whitespace
    # classes, digit suffixes, repeated occurrences, whitespace-only <private>.
    "api_key=abcdefgh1234x then {\"log\":\"login ok\\nabcdefgh1234x\\n\"} and \\tabcdefgh1234x and \\r\\nabcdefgh1234x and \\\"abcdefgh1234x and \"abcdefgh1234x and \\u0041abcdefgh1234x and q=a%20abcdefgh1234x and %0Aabcdefgh1234x and nabcdefgh1234x and %abcdefgh1234x",
    "client_secret: Lq7x9mK2pR4vB8nT1wZ5cF and Lq7x9mK2pR4vB8nT1wZ5cF.Lq7x9mK2pR4vB8nT1wZ5cF and Lq7x9mK2pR4vB8nT1wZ5cFLq7x9mK2pR4vB8nT1wZ5cF and Lq7x9mK2pR4vB8nT1wZ5cF.txt and xLq7x9mK2pR4vB8nT1wZ5cFy",
    "token=abc12345 and token_abc12345: x and abc12345.example.com and /tmp/abc12345-backup and www.abc12345 and abc12345. end and see abc12345, ok",
    "password=!x9y8z7w6 and again !x9y8z7w6 and a!x9y8z7w6",
    "token=ab12 ab12 and ab123",
    "key sk-ant-api03-AbCdEf_GhIj-KlMnOpQrStUvWxYz0123456789 repeated sk-ant-api03-AbCdEf_GhIj-KlMnOpQrStUvWxYz0123456789 and password=hunter2hunter2 hunter2hunter2.",
    "token2=abcd1234 password1=hunter2hunter2 PASSWORD_V2=abc12345xyz DB_PASSWORD_2=zzzz98765 and \"clientSecret3\": \"qwertyuiop\" and token_v3=abcd5678 id_token1: abcd9999",
    "version=abcd1234 tokenize=abcd1234 mytoken2=abcd1234 api_key2=abcd1234 apikeyV10=abcd1234 secret_key3=abcd1234 TOKEN_2=abcd1234",
    "APIKey=`cqj0i8smq9574`\ufeffpasswd\xa0=\xa07aekz95ys86v9576",
    "token=\x1cabcd1234 and secret=abcd5678\x85tail1234 and pwd=xyzw9876\u2028x",
    "token=\ufeffabcd1234 and token=\u00a0abcd1234 and token=\u2003abcd1234 and token=\u3000abcd1234 and token=\u180eabcd1234",
    "X-Api-Key: abcdef1234567890\ufeffAuthorization:\xa0Bearer\xa0zyxwvu9876543210",
    "authorization\u2003:\u3000bearer\u00a0abcdef1234567890 and x-auth-token\u0085: abcdef1234567890",
    "\"api_key\"\ufeff:\xa0\"abcdefgh12\" and \"token\"\x1c:\"abcdefgh12\"",
    "hello <private>\n",
    "hello <private>  \n\nworld token=abcd1234",
    "hello <private>   </private> and <private>\t</private> then token=abcd1234",
    "<private>NAME=   </private> tail",
    "<private>\n</private>\nline one\nline two\n\tindented\n",
    "password = os.environ.get(\ntoken = strings.Trim(\npassword = lexer.get_token()\npwd = os.getcwd()\ntoken = self.get_token()\npasswd = getpass.getpass(\nsignature = inspect.signature(",
    "PASSWORD=Passw0rd(2024 password=Summer2024(!x DB_PASSWORD=hunter2.abc(de1 pwd=abc(12345678 token=Zx9kQ2mP7(wLr password=correct.horse(battery",
    "token=getX(password=hunter2hunter2 token=getX(password=hunter2hunter2 ",
    "-----BEGIN PRIVATE KEY-----" * 5,
    "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n-----END PRIVATE KEY----- tail -----BEGIN PRIVATE KEY-----\nAAAA\n",
    "-----END PRIVATE KEY----- stray end -----BEGIN PRIVATE KEY-----",
    "a" + " " * 3000 + "=x",
    "token=f(" * 200,
    "a=" * 400,
    "password=abcdefg1 password=abcdefg2 password=abcdefg3 " * 3 + "abcdefg1 abcdefg2 abcdefg3",
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
doc = {"about": "Golden cases produced by running the Pegasus redactor (engram-hook.py cut_to_detect_cap + redact, Pegasus 7.9.2 catalog) with _token -> [REDACTED:NAME] and one extra placeholder pattern ^\\[REDACTED:[A-Z0-9_]+\\]$. All values are synthetic. Regenerate with the generator when the catalog changes.", "cases": cases}
json.dump(doc, open(out_path, "w"), indent=2, ensure_ascii=False)
open(out_path, "a").write("\n")
print(len(old["cases"]), "old,", len(cases) - len(old["cases"]), "new; old expected changed by the oracle:", changed)
