"""Explicit install-time model population; no provider keys are needed."""
import sys
from fastembed import TextEmbedding
TextEmbedding(model_name="BAAI/bge-small-en-v1.5", cache_dir=sys.argv[1])
from fastembed import SparseTextEmbedding
SparseTextEmbedding(model_name="Qdrant/bm25", cache_dir=sys.argv[1])  # mem0 qdrant store hybrid BM25 encoder (optional; warns without it)
