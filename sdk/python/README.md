# Runbox Python SDK

```bash
pip install runbox-sdk
```

```python
from runbox import Client, Identity

client = Client(
    "http://localhost:8099",
    api_key="...",
    identity=Identity(namespace="team-a", principal="user-42"),
)
result = client.exec('python3 -c "print(6 * 7)"')
print(result["stdout"])
```

The client uses only the Python standard library. Uploads stream from disk;
downloads and tar archives stream to a destination path.

Full API reference: [docs/api.md](../../docs/api.md).
