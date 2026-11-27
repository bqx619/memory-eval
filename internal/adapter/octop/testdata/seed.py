"""Offline test fixtures only. Real capture/search/get/promote use the bridge RPC.

1.0.1 has no add-candidate/page RPC; seed via its public Python API, never by
patching installed code. No extract or LLM client is invoked.
"""
import json
import sys
from datetime import UTC, datetime
from octop_memory.core import Memory
from octop_memory.types import Alias, Candidate, Entity, EntityPage

db, ns, kind = sys.argv[1:]
m = Memory(namespace=ns, backend="sqlite", backend_config={"db_path": db})
now = datetime(2023, 5, 8, 13, 56, tzinfo=UTC)
if kind == "page":
    m.add_entity(Entity("entity-page", "Project", "Zirconia", [], 0, now))
    m.add_alias(Alias("zirconia", "entity-page", "Project", "user", now))
    m.upsert_entity_page(EntityPage("page-contract", "entity-page", "Zirconia project page", "Zirconia celestial launch schedule", [], False, 0, 1, now, now))
    print(json.dumps({"page_id": "page-contract"}))
else:
    ids = ["evt-a"] if kind == "atom" else ["evt-a", "evt-b", "evt-a"]
    m.add_candidates([Candidate(
        id="candidate-contract", raw_event_ids=ids, candidate_type="Fact", status="needs_review",
        title="quartzviolet", assertion="quartzviolet telescope uses a cobalt lens",
        verbatim_quote="quartzviolet telescope uses a cobalt lens", quote_event_id="evt-a",
        subject_name="Telescope", subject_entity_type="Project", target_entity_id=None,
        confidence="high", importance="medium", recommended_action="create_atom",
        promotion_reason="offline contract fixture", extractor_version="test", created_at=now,
    )])
    print(json.dumps({"candidate_id": "candidate-contract"}))
