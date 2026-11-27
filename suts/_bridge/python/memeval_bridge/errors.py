"""Preserve governor stop classification across SDK and HTTP exception wrappers."""
import json
import re


def error_data(error):
    data = {'type': type(error).__name__}
    public = getattr(error, 'public_error_data', None)
    if callable(public):
        # An explicit adapter contract; never copy arbitrary response bodies.
        details = public()
        if isinstance(details, dict):
            data['details'] = details
    seen = set()

    def inspect(value):
        if value is None or id(value) in seen:
            return None
        seen.add(id(value))
        if isinstance(value, dict):
            headers = {str(k).lower(): v for k, v in value.items()}
            if headers.get('x-memeval-governor') in ('exhausted', 'budget'):
                return headers['x-memeval-governor']
            if value.get('type') == 'memeval_governor' and value.get('reason') in ('exhausted', 'budget'):
                return value['reason']
            return inspect(value.get('error')) or inspect(value.get('body'))
        if isinstance(value, bytes):
            value = value.decode('utf-8', errors='replace')
        if isinstance(value, str):
            try:
                parsed = json.loads(value)
            except (ValueError, TypeError):
                parsed = None
            reason = inspect(parsed)
            if reason:
                return reason
            if 'memeval_governor' in value:
                match = re.search(r'''["']?reason["']?\s*:\s*["'](exhausted|budget)["']''', value)
                if match:
                    return match[1]
            match = re.search(r'''(?i)x-memeval-governor["']?\s*:\s*["']?(exhausted|budget)\b''', value)
            return match[1].lower() if match else None
        headers = getattr(value, 'headers', None)
        if headers is not None:
            reason = inspect(dict(headers))
            if reason:
                return reason
        for key in ('response', 'body', 'error', '__cause__', '__context__'):
            reason = inspect(getattr(value, key, None))
            if reason:
                return reason
        return inspect(str(value))

    reason = inspect(error)
    if reason:
        data['governor'] = reason
    return data
