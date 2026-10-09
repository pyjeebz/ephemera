"""Generate docs/architecture.tldr for the Ephemera architecture diagram."""

from __future__ import annotations

import json
from pathlib import Path

OUT = Path("docs/architecture.tldr")

SCHEMA = {
    "schemaVersion": 2,
    "sequences": {
        "com.tldraw.store": 5,
        "com.tldraw.asset": 1,
        "com.tldraw.camera": 1,
        "com.tldraw.document": 2,
        "com.tldraw.instance": 26,
        "com.tldraw.instance_page_state": 5,
        "com.tldraw.page": 1,
        "com.tldraw.instance_presence": 6,
        "com.tldraw.pointer": 1,
        "com.tldraw.shape": 4,
        "com.tldraw.user": 1,
        "com.tldraw.asset.image": 6,
        "com.tldraw.asset.video": 5,
        "com.tldraw.asset.bookmark": 2,
        "com.tldraw.shape.arrow": 8,
        "com.tldraw.shape.bookmark": 2,
        "com.tldraw.shape.draw": 5,
        "com.tldraw.shape.embed": 4,
        "com.tldraw.shape.frame": 1,
        "com.tldraw.shape.geo": 12,
        "com.tldraw.shape.group": 0,
        "com.tldraw.shape.highlight": 4,
        "com.tldraw.shape.image": 5,
        "com.tldraw.shape.line": 5,
        "com.tldraw.shape.note": 13,
        "com.tldraw.shape.text": 4,
        "com.tldraw.shape.video": 4,
        "com.tldraw.binding.arrow": 1,
    },
}


def rich(text: str) -> dict:
    return {
        "type": "doc",
        "content": [
            {
                "type": "paragraph",
                "content": [{"type": "text", "text": text}] if text else [],
            }
        ],
    }


def shape(id_: str, type_: str, x: float, y: float, props: dict) -> dict:
    return {
        "id": f"shape:{id_}",
        "typeName": "shape",
        "type": type_,
        "x": x,
        "y": y,
        "rotation": 0,
        "index": "a1",
        "parentId": "page:page1",
        "isLocked": False,
        "opacity": 1,
        "meta": {},
        "props": props,
    }


def geo(id_: str, x: float, y: float, w: float, h: float, label: str,
        color: str = "black", fill: str = "none") -> dict:
    return shape(id_, "geo", x, y, {
        "geo": "rectangle",
        "color": color,
        "fill": fill,
        "dash": "draw",
        "size": "m",
        "font": "mono",
        "align": "middle",
        "verticalAlign": "middle",
        "labelColor": color,
        "w": w,
        "h": h,
        "richText": rich(label),
        "growY": 0,
        "url": "",
        "scale": 1,
        "flipX": False,
        "flipY": False,
    })


def text(id_: str, x: float, y: float, s: str, color: str = "black",
         size: str = "m", w: float = 200) -> dict:
    return shape(id_, "text", x, y, {
        "color": color,
        "font": "mono",
        "size": size,
        "textAlign": "start",
        "autoSize": False,
        "w": w,
        "scale": 1,
        "richText": rich(s),
    })


def arrow(id_: str, x: float, y: float, w: float, h: float,
          label: str = "", color: str = "grey") -> dict:
    return shape(id_, "arrow", x, y, {
        "color": color,
        "dash": "draw",
        "size": "m",
        "fill": "solid",
        "bend": 0,
        "scale": 1,
        "kind": "arc",
        "labelColor": "black",
        "arrowheadStart": "none",
        "arrowheadEnd": "arrow",
        "font": "mono",
        "start": {"x": 0, "y": 0},
        "end": {"x": w, "y": h},
        "richText": rich(label),
        "labelPosition": 0.5,
        "elbowMidPoint": 0.5,
    })


def build() -> dict:
    shapes = [
        text("title", 60, 40,
             "Ephemera — link before upload", size="l", w=900),
        text("subtitle", 60, 100,
             "A shareable link exists first; the sender daemon streams only requested byte ranges. "
             "The relay keeps no durable file.", color="grey", w=900),

        geo("sender", 60, 180, 300, 220,
            "Sender device\n\n- file on disk\n- ephemera send daemon\n- keep-awake\n- resume state"),
        geo("relay", 500, 180, 400, 220,
            "Relay\n\n- /daemon WebSocket bridge\n- registry + TTL sweeper\n"
            "- receiver HTTP surface\n- rate limit + metrics"),
        geo("receiver", 1060, 180, 300, 220,
            "Receiver browser\n\n- /:id page\n- /:id/status polling\n- /:id/file Range requests"),

        geo("state", 60, 460, 300, 100,
            "Share state\nresume after restart", color="grey"),
        geo("cache", 500, 460, 180, 100,
            "Layer B\nvolatile cache\n(RAM only)", color="grey"),
        geo("away", 720, 460, 180, 100,
            "Layer C\naway prefix\n(first N bytes)", color="red"),
        geo("admin", 1060, 460, 300, 100,
            "Admin + metrics\nrate limit · kill · counters", color="grey"),

        arrow("sender_to_relay", 360, 290, 140, 0, "WebSocket"),
        arrow("relay_to_receiver", 900, 290, 160, 0, "HTTP Range"),
        arrow("sender_to_state", 210, 400, 0, 60, "state"),
        arrow("relay_to_cache", 560, 400, 0, 60, "write-through"),
        arrow("relay_to_away", 810, 400, 0, 60, "trickle"),
        arrow("cache_to_receiver", 680, 510, 380, -120, "offline replay"),
        arrow("away_to_receiver", 900, 510, 160, -80, "offline prefix"),
        arrow("relay_to_admin", 900, 400, 160, 110, "admin"),

        text("layers", 60, 620,
             "Layer A: live byte-range streaming · Layer B: volatile RAM replay · "
             "Layer C: away prefix\nNo durable storage — the TTL sweeper drops the share, cache, and prefix at expiry.",
             color="grey", w=1200),
    ]

    page = {
        "id": "page:page1",
        "typeName": "page",
        "name": "Ephemera architecture",
        "index": "a1",
        "meta": {},
    }
    camera = {
        "id": "camera:page1",
        "typeName": "camera",
        "x": 40,
        "y": 20,
        "z": 0.65,
        "meta": {},
    }
    instance_page_state = {
        "id": "instance_page_state:page1",
        "typeName": "instance_page_state",
        "pageId": "page:page1",
        "editingShapeId": None,
        "croppingShapeId": None,
        "selectedShapeIds": [],
        "hintingShapeIds": [],
        "erasingShapeIds": [],
        "hoveredShapeId": None,
        "focusedGroupId": None,
        "meta": {},
    }

    return {
        "tldrawFileFormatVersion": 1,
        "schema": SCHEMA,
        "records": [page, camera, instance_page_state, *shapes],
    }


if __name__ == "__main__":
    doc = build()
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(doc, indent=1))
    print(f"wrote {OUT} ({len(doc['records'])} records)")
