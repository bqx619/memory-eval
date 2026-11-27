"""Skeleton ONLY: not an implemented integration for any named PA."""
from pa_memory_eval.core import UnsupportedCapability

class YourMemoryAdapter:
    def describe(self):
        return {"name":"your-pa", "version":"PIN-YOUR-COMMIT", "capabilities":["retrieve"],
                "config":{"backend":"replace-this"},"read_only_queries":True}
    def reset(self,namespace):
        raise NotImplementedError("Create/clear only this temporary benchmark namespace")
    def ingest(self,namespace,events):
        raise NotImplementedError("Write source events; retain mapping from returned memory to source_ids")
    def ready(self,namespace):
        raise NotImplementedError("Wait until ALL ingested events are query-visible, including background extraction")
    def retrieve(self,namespace,request):
        raise NotImplementedError("Search only the authenticated request user/scope; obey budgets inside your system")
    def answer(self,namespace,request):
        raise UnsupportedCapability("Implement and advertise answer only when actual PA response is available")
    def close(self): pass

def create(**config):
    return YourMemoryAdapter()
