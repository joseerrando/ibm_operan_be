"""Membuat (atau memperbarui) 5 flow Operan di Langflow lewat API, lalu mengekspornya ke flows/*.json.

Pemakaian:
    python flows/build_flows.py                     # Langflow di http://127.0.0.1:7860 dengan auto-login
    LANGFLOW_URL=... LANGFLOW_API_KEY=... python flows/build_flows.py

Komponen diambil dari katalog Langflow yang sedang berjalan (/api/v1/all), sehingga cocok dengan versinya.
Model: Google Generative AI (Gemini) dengan API key dari variabel global Langflow GOOGLE_API_KEY.
Prompt diambil dari blok kode pertama di flows/prompts/0X-*.md.
"""

from __future__ import annotations

import copy
import gzip
import json
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
BASE = os.environ.get("LANGFLOW_URL", "http://127.0.0.1:7860").rstrip("/")
GOOGLE_COMPONENT = "ext:google:GoogleGenerativeAIComponent@official"
MODEL = os.environ.get("GEMINI_MODEL", "gemini-3.1-flash-lite-preview")
INTERNAL_TOKEN = os.environ.get("INTERNAL_TOOL_TOKEN", "")

FLOWS = [
    ("extract_voice_log", "01-extract-voice.md", "Operan 1 — ekstraksi catatan suara"),
    ("handover_summary", "02-handover.md", "Operan 2 — ringkasan operan"),
    ("trend_analysis", "03-trend.md", "Operan 3 — analisis tren"),
    ("doctor_report", "04-doctor-report.md", "Operan 4 — laporan dokter"),
    ("ask_history", "05-ask-history.md", "Operan 5 — agent tanya riwayat"),
]


# ---------------------------------------------------------------- HTTP

def _headers() -> dict[str, str]:
    h = {"Content-Type": "application/json", "Accept-Encoding": "identity"}
    key = os.environ.get("LANGFLOW_API_KEY")
    if key:
        h["x-api-key"] = key
    else:
        tok = request("GET", "/api/v1/auto_login", auth=False)["access_token"]
        h["Authorization"] = f"Bearer {tok}"
    return h


_H: dict[str, str] | None = None


def request(method: str, path: str, body=None, auth: bool = True):
    global _H
    headers = {"Accept-Encoding": "identity", "Content-Type": "application/json"}
    if auth:
        if _H is None:
            _H = _headers()
        headers = _H
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read()
    except urllib.error.HTTPError as e:
        raise SystemExit(f"{method} {path} -> {e.code}: {e.read()[:800].decode(errors='replace')}")
    if raw[:2] == bytes([0x1F, 0x8B]):
        raw = gzip.decompress(raw)
    return json.loads(raw) if raw else None


# ---------------------------------------------------------------- nodes

CATALOG: dict = {}


def component(key: str) -> dict:
    for comps in CATALOG.values():
        if key in comps:
            return copy.deepcopy(comps[key])
    raise SystemExit(f"Komponen {key} tidak ada di katalog Langflow ini.")


def node(node_id: str, key: str, x: int, y: int, tpl: dict | None = None) -> dict:
    comp = component(key)
    for field, value in (tpl or {}).items():
        comp["template"][field]["value"] = value
    return {
        "id": node_id,
        "type": "genericNode",
        "position": {"x": x, "y": y},
        "data": {"id": node_id, "type": key, "node": comp, "showNode": True},
    }


def edge(src: dict, out_name: str, dst: dict, field: str) -> dict:
    out = next(o for o in src["data"]["node"]["outputs"] if o["name"] == out_name)
    tf = dst["data"]["node"]["template"][field]
    source_handle = {"dataType": src["data"]["type"], "id": src["id"], "name": out_name, "output_types": out.get("types", [])}
    target_handle = {"fieldName": field, "id": dst["id"], "inputTypes": tf.get("input_types") or [], "type": tf.get("type", "str")}
    # Langflow UI (React Flow) needs a unique edge id and the handles as "œ"-escaped JSON strings,
    # otherwise edges sharing an empty id collapse into one drawn line.
    sh, th = _handle_str(source_handle), _handle_str(target_handle)
    return {
        "id": f"reactflow__edge-{src['id']}{sh}-{dst['id']}{th}",
        "source": src["id"],
        "target": dst["id"],
        "sourceHandle": sh,
        "targetHandle": th,
        "animated": False,
        "className": "",
        "data": {"sourceHandle": source_handle, "targetHandle": target_handle},
    }


def _handle_str(handle: dict) -> str:
    return json.dumps(handle, separators=(",", ":"), ensure_ascii=False).replace('"', "œ")


def prompt_text(md_file: str) -> str:
    text = (HERE / "prompts" / md_file).read_text(encoding="utf-8")
    block = re.search(r"```\n(.*?)\n```", text, re.S)
    if not block:
        raise SystemExit(f"Blok prompt tidak ditemukan di {md_file}")
    return block.group(1)


def google_model(node_id: str, x: int, y: int) -> dict:
    n = node(node_id, GOOGLE_COMPONENT, x, y, {"model_name": MODEL, "temperature": 0.1})
    key = n["data"]["node"]["template"]["api_key"]
    key["value"] = "GOOGLE_API_KEY"  # nama variabel global Langflow
    key["load_from_db"] = True
    return n


def prompt_node(node_id: str, template: str, x: int, y: int) -> dict:
    n = node(node_id, "Prompt Template", x, y)
    t = n["data"]["node"]["template"]
    t["use_double_brackets"]["value"] = True
    t["template"]["value"] = template.replace("{payload}", "{{payload}}")
    t["payload"] = {
        "advanced": False, "display_name": "payload", "dynamic": False, "field_type": "str", "fileTypes": [],
        "file_path": "", "info": "", "input_types": ["Message"], "list": False, "load_from_db": False,
        "multiline": True, "name": "payload", "placeholder": "", "required": False, "show": True,
        "title_case": False, "type": "str", "value": "",
    }
    n["data"]["node"]["custom_fields"] = {"template": ["payload"]}
    return n


def simple_flow(template: str) -> dict:
    ci = node("ChatInput-op1", "ChatInput", 0, 200, {"should_store_message": False})
    pr = prompt_node("Prompt-op1", template, 380, 160)
    lm = google_model("Gemini-op1", 760, 140)
    co = node("ChatOutput-op1", "ChatOutput", 1140, 200, {"should_store_message": False})
    return {
        "nodes": [ci, pr, lm, co],
        "edges": [
            edge(ci, "message", pr, "payload"),
            edge(pr, "prompt", lm, "input_value"),
            edge(lm, "text_output", co, "input_value"),
        ],
    }


def tool_mode(n: dict) -> dict:
    """Ask Langflow to convert a component to tool mode (same call the UI makes)."""
    comp = n["data"]["node"]
    res = request("POST", "/api/v1/custom_component/update", {
        "code": comp["template"]["code"]["value"],
        "template": comp["template"],
        "field": "tool_mode",
        "field_value": True,
        "tool_mode": True,
    })
    comp.update(res)
    comp["tool_mode"] = True
    return n


def agent_flow(instructions: str) -> dict:
    ci = node("ChatInput-op5", "ChatInput", 0, 260, {"should_store_message": False})
    lm = google_model("Gemini-op5", 0, -120)
    lm["data"]["node"]["template"]["tool_model_enabled"]["value"] = True
    headers = [{"key": "User-Agent", "value": "Langflow/1.0"}, {"key": "X-Internal-Token", "value": INTERNAL_TOKEN}]
    api = node("APIRequest-op5", "APIRequest", 380, 420, {"method": "GET", "headers": headers, "timeout": 20})
    api = tool_mode(api)
    ag = node("Agent-op5", "Agent", 760, 160, {
        "system_prompt": instructions,
        "add_calculator_tool": False,
        "add_current_date_tool": False,
        "max_iterations": 8,
        "stream": False,
    })
    co = node("ChatOutput-op5", "ChatOutput", 1140, 220, {"should_store_message": False})
    tool_out = next(o["name"] for o in api["data"]["node"]["outputs"] if "Tool" in (o.get("types") or []))
    return {
        "nodes": [ci, lm, api, ag, co],
        "edges": [
            edge(ci, "message", ag, "input_value"),
            edge(lm, "model_output", ag, "model"),
            edge(api, tool_out, ag, "tools"),
            edge(ag, "response", co, "input_value"),
        ],
    }


# ---------------------------------------------------------------- main

PROJECT_NAME = "Operan"


def ensure_project() -> str:
    """Flow Operan selalu disimpan di project Langflow sendiri, bukan di Starter Project."""
    for p in request("GET", "/api/v1/projects/"):
        if p["name"] == PROJECT_NAME:
            return p["id"]
    p = request("POST", "/api/v1/projects/", {
        "name": PROJECT_NAME,
        "description": "Lima flow AI Operan: ekstraksi suara, ringkasan operan, analisis tren, laporan dokter, agent tanya riwayat.",
        "components_list": [], "flows_list": [],
    })
    return p["id"]


def main() -> None:
    CATALOG.update(request("GET", "/api/v1/all"))
    project_id = ensure_project()
    existing = {f["name"]: f["id"] for f in request("GET", "/api/v1/flows/?get_all=true&header_flows=true")}
    env_names = {
        "extract_voice_log": "FLOW_ID_EXTRACT_VOICE", "handover_summary": "FLOW_ID_HANDOVER",
        "trend_analysis": "FLOW_ID_TREND", "doctor_report": "FLOW_ID_DOCTOR_REPORT", "ask_history": "FLOW_ID_ASK_HISTORY",
    }
    only = set(sys.argv[1:])
    for name, md, desc in FLOWS:
        if only and name not in only:
            continue
        text = prompt_text(md)
        data = agent_flow(text) if name == "ask_history" else simple_flow(text)
        body = {"name": name, "description": desc, "data": data, "is_component": False, "folder_id": project_id}
        if name in existing:
            flow = request("PATCH", f"/api/v1/flows/{existing[name]}", body)
        else:
            flow = request("POST", "/api/v1/flows/", body)
        export = {k: flow[k] for k in ("name", "description", "data") if k in flow}
        text_out = json.dumps(export, indent=2, ensure_ascii=False)
        if INTERNAL_TOKEN:
            text_out = text_out.replace(INTERNAL_TOKEN, "<INTERNAL_TOOL_TOKEN>")  # jangan commit rahasia
        (HERE / f"{name}.json").write_text(text_out, encoding="utf-8")
        print(f"{env_names[name]}={flow['id']}")


if __name__ == "__main__":
    main()
