# BEAM conformance fixture

Source: Mohammadta/BEAM, revision 3205395e897e7318c7b094ef4e6047b9b82dbb03, 100K first conversation. Attribution: Mohammad Tavakoli, Alireza Salemi, Carrie Ye, Mohamed Abdalla, Hamed Zamani, J Ross Mitchell, *Beyond a Million Tokens: Benchmarking and Enhancing Long-Term Memory in LLMs* (2025).

Data license: **CC BY-SA 4.0**, https://creativecommons.org/licenses/by-sa/4.0/. This modified fixture and its normalized goldens retain that license. Upstream code license is independently MIT.

Reproduce offline: `GOPROXY=off go run scripts/generate_beam_fixture.go`.
Changes: retain the first chat batch (60 messages), retain the first question in each of ten categories, discard source_chat_ids outside that batch, project only fields consumed by the importer. Original question/rubric/content text is unedited. This is a truncated contract fixture, not an evaluation corpus.
