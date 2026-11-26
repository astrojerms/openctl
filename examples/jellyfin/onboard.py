#!/usr/bin/env python3
"""Configure Jellyfin locally; read admin/roku username/password objects on stdin."""
import json
import sys
from urllib.parse import urlencode
from urllib.request import Request, urlopen

credentials = json.load(sys.stdin)
base = "http://127.0.0.1:8096"
headers = {
    "Content-Type": "application/json",
    "Authorization": 'MediaBrowser Client="OpenCTL", Device="Setup", DeviceId="openctl-setup", Version="1.0"',
}


def api(path, body=None, method=None):
    request = Request(
        base + path,
        data=None if body is None else json.dumps(body).encode(),
        headers=headers,
        method=method or ("GET" if body is None else "POST"),
    )
    with urlopen(request, timeout=120) as response:
        data = response.read()
    return json.loads(data) if data else None


admin = credentials["admin"]
if not api("/System/Info/Public")["StartupWizardCompleted"]:
    # Jellyfin creates its initial account when the wizard requests FirstUser.
    api("/Startup/FirstUser")
    api("/Startup/Configuration", {
        "ServerName": "Synology Media",
        "UICulture": "en-US",
        "MetadataCountryCode": "US",
        "PreferredMetadataLanguage": "en",
    })
    api("/Startup/User", {"Name": admin["username"], "Password": admin["password"]})
    api("/Startup/RemoteAccess", {"EnableRemoteAccess": False})
    api("/Startup/Complete", method="POST")

login = api("/Users/AuthenticateByName", {"Username": admin["username"], "Pw": admin["password"]})
headers["Authorization"] += ', Token="' + login["AccessToken"] + '"'
try:
    admin_policy = login["User"]["Policy"]
    admin_policy["IsHidden"] = True
    api("/Users/" + login["User"]["Id"] + "/Policy", admin_policy)

    viewer = credentials["roku"]
    users = api("/Users")
    roku = next((user for user in users if user["Name"] == viewer["username"]), None)
    if roku is None:
        roku = api("/Users/New", {"Name": viewer["username"], "Password": viewer["password"]})
    policy = roku["Policy"]
    policy.update({
        "IsAdministrator": False,
        "IsHidden": False,
        "EnableRemoteAccess": False,
        "EnableContentDeletion": False,
        "EnableAllFolders": True,
        "EnableAudioPlaybackTranscoding": True,
        "EnableVideoPlaybackTranscoding": True,
        "EnablePlaybackRemuxing": True,
    })
    api("/Users/" + roku["Id"] + "/Policy", policy)

    encoding = api("/System/Configuration/encoding")
    encoding.update({
        "HardwareAccelerationType": "nvenc",
        "EnableHardwareEncoding": True,
        "EnableEnhancedNvdecDecoder": True,
        "HardwareDecodingCodecs": ["h264", "hevc", "mpeg2video", "vc1", "vp8", "vp9"],
        "EnableDecodingColorDepth10Hevc": True,
        "EnableDecodingColorDepth10Vp9": True,
        "EnableTonemapping": True,
        "TonemappingAlgorithm": "bt2390",
        "TranscodingTempPath": "/cache/transcodes",
    })
    api("/System/Configuration/encoding", encoding)

    folders = api("/Library/VirtualFolders")
    if not any("/media" in folder.get("Locations", []) for folder in folders):
        query = urlencode({"name": "Synology Videos", "collectionType": "mixed", "paths": "/media", "refreshLibrary": "true"})
        api("/Library/VirtualFolders?" + query, {"LibraryOptions": {
            "Enabled": True,
            "PathInfos": [{"Path": "/media"}],
            "EnableRealtimeMonitor": False,
            "SaveLocalMetadata": False,
            "EnableChapterImageExtraction": False,
            "EnableTrickplayImageExtraction": False,
        }})
    else:
        api("/Library/Refresh", method="POST")
    print("Admin and non-admin Roku accounts configured; remote access disabled.")
    print("NVENC/NVDEC and CUDA tone mapping enabled; Synology Videos scan requested.")
finally:
    api("/Sessions/Logout", method="POST")
