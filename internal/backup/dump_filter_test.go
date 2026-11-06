package backup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const logsDataSection = "--\n-- Data for Name: logs; Type: TABLE DATA; Schema: public; Owner: -\n--\n\n"

func TestFilterDumpOnlyRemovesTopLevelLogAudit(t *testing.T) {
	input := "CREATE TABLE public.logs (id bigint, other text, quota integer);\n" +
		logsDataSection +
		"COPY public.logs (id, other, quota) FROM stdin;\n" +
		"1\t" + `{"po":["private prompt"],"quota":9007199254740993,"nested":{"po":"keep"}}` + "\t42\n" +
		"2\t" + ` {"quota":17} ` + "\t17\n" +
		"3\t\\N\t0\n4\t\t0\n5\tnull\t0\n6\t[]\t0\n" +
		"\\.\n\n" +
		"-- Data for Name: channels; Type: TABLE DATA; Schema: public; Owner: -\n\n" +
		"COPY public.channels (id, param_override) FROM stdin;\n" +
		"1\t" + `{"po":"keep config","messages":["private prompt"]}` + "\n\\.\n" +
		"SELECT pg_catalog.setval('public.logs_id_seq', 6, true);\n"
	want := strings.Replace(input,
		`{"po":["private prompt"],"quota":9007199254740993,"nested":{"po":"keep"}}`,
		`{"nested":{"po":"keep"},"quota":9007199254740993}`, 1)
	var output bytes.Buffer
	if err := filterDump(&output, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if output.String() != want {
		t.Fatalf("filtered dump differs:\ngot  %q\nwant %q", output.String(), want)
	}
}

func TestFilterDumpLocatesQuotedAndReorderedColumns(t *testing.T) {
	input := logsDataSection + "COPY \"public\".\"logs\" (\"other\", id, \"group\") FROM stdin;\r\n" +
		`{"po":["discard"],"keep":true}` + "\t1\tdefault\r\n\\.\r\n"
	want := strings.Replace(input, `{"po":["discard"],"keep":true}`, `{"keep":true}`, 1)
	var output bytes.Buffer
	if err := filterDump(&output, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if output.String() != want {
		t.Fatalf("quoted COPY changed unexpectedly: %q", output.String())
	}
}

func TestFilterDumpPreservesEscapedJSONAndLargeRows(t *testing.T) {
	input := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n" +
		"1\t" + `{"po":["` + strings.Repeat("x", 128*1024) + `"],"text":"中文\\\\path\\tline\\nnext\\rreturn\\bback\\fform\\u000bvertical","literal":"\\\\N"}` +
		"\n\\.\n"
	wantOther := `{"literal":"\\\\N","text":"中文\\\\path\\tline\\nnext\\rreturn\\bback\\fform\\u000bvertical"}`
	want := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\t" + wantOther + "\n\\.\n"
	var output bytes.Buffer
	if err := filterDump(&output, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if output.String() != want {
		t.Fatalf("escaped fields were not preserved (output length %d)", output.Len())
	}
}

func TestFilterDumpHandlesPostgresEscapesInJSONKeys(t *testing.T) {
	for name, inputOther := range map[string]string{
		"COPY octal":   `{"\160\157":["discard"],"keep":1}`,
		"COPY hex":     `{"\x70\x6f":["discard"],"keep":1}`,
		"JSON unicode": `{"\\u0070o":["discard"],"keep":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\t" + inputOther + "\n\\.\n"
			want := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\t" + `{"keep":1}` + "\n\\.\n"
			var output bytes.Buffer
			if err := filterDump(&output, strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			if output.String() != want {
				t.Fatalf("escaped po key was not removed: %q", output.String())
			}
		})
	}
}

func TestFilterDumpLeavesOtherSQLAndSchemasUntouched(t *testing.T) {
	for name, input := range map[string]string{
		"function body":    "CREATE FUNCTION public.example() RETURNS text AS $$\nCOPY public.logs (other) FROM stdin;\nnot JSON\n\\.\n$$ LANGUAGE sql;\n",
		"other schema":     "-- Data for Name: logs; Type: TABLE DATA; Schema: audit; Owner: -\n\nCOPY audit.logs (other) FROM stdin;\nnot JSON\n\\.\n",
		"no other column":  logsDataSection + "COPY public.logs (id) FROM stdin;\n1\n\\.\n",
		"no final newline": "SELECT 1;",
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := filterDump(&output, strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			if output.String() != input {
				t.Fatal("unrelated SQL was modified")
			}
		})
	}
}

func TestFilterDumpRejectsMalformedLogData(t *testing.T) {
	for name, data := range map[string]string{
		"invalid JSON":            "1\tprivate prompt is not JSON\n\\.\n",
		"invalid UTF8":            "1\t{\"po\":[],\"keep\":\"\xff\"}\n\\.\n",
		"missing column":          "1\n\\.\n",
		"incomplete COPY escape":  "1\t{}\\\n\\.\n",
		"invalid COPY hex":        "1\t\\x\n\\.\n",
		"missing COPY terminator": "1\t{}\n",
	} {
		t.Run(name, func(t *testing.T) {
			input := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n" + data
			var output bytes.Buffer
			err := filterDump(&output, strings.NewReader(input))
			if err == nil {
				t.Fatal("malformed backup was accepted")
			}
			if strings.Contains(err.Error(), "private prompt") {
				t.Fatal("error includes private log content")
			}
		})
	}
}

type failingDumpReader struct{}

func (failingDumpReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type failingDumpWriter struct{}

func (failingDumpWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type shortDumpWriter struct{}

func (shortDumpWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestFilterDumpPropagatesIOFailures(t *testing.T) {
	if err := filterDump(io.Discard, failingDumpReader{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v", err)
	}
	if err := filterDump(failingDumpWriter{}, strings.NewReader("SELECT 1;\n")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
	if err := filterDump(shortDumpWriter{}, strings.NewReader("SELECT 1;\n")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
}
