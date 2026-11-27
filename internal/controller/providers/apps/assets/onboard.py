#!/usr/bin/env python3
"""Configure Jellyfin locally; read admin/roku username/password objects on stdin."""
import json
import sys
from urllib.parse import urlencode
from urllib.request import Request, urlopen

credentials = json.load(sys.stdin)
allow_remote_access = credentials.get("allowRemoteAccess", False) is True
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
        "ServerName": "Media Library",
        "UICulture": "en-US",
        "MetadataCountryCode": "US",
        "PreferredMetadataLanguage": "en",
    })
    api("/Startup/User", {"Name": admin["username"], "Password": admin["password"]})
    api("/Startup/RemoteAccess", {"EnableRemoteAccess": allow_remote_access, "EnableAutomaticPortMapping": False})
    api("/Startup/Complete", method="POST")

login = api("/Users/AuthenticateByName", {"Username": admin["username"], "Pw": admin["password"]})
headers["Authorization"] += ', Token="' + login["AccessToken"] + '"'
try:
    network = api("/System/Configuration/network")
    network["EnableRemoteAccess"] = allow_remote_access
    api("/System/Configuration/network", network)
    admin_policy = login["User"]["Policy"]
    admin_policy["IsHidden"] = True
    admin_policy["EnableRemoteAccess"] = allow_remote_access
    api("/Users/" + login["User"]["Id"] + "/Policy", admin_policy)

    viewer = credentials["roku"]
    users = api("/Users")
    roku = next((user for user in users if user["Name"] == viewer["username"]), None)
    if roku is None:
        roku = api("/Users/New", {"Name": viewer["username"], "Password": viewer["password"]})
    else:
        api("/Users/" + roku["Id"] + "/Password", {"NewPw": viewer["password"]})
    policy = roku["Policy"]
    policy.update({
        "IsAdministrator": False,
        "IsHidden": False,
        "EnableRemoteAccess": allow_remote_access,
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
        query = urlencode({"name": "Media Library", "collectionType": "mixed", "paths": "/media", "refreshLibrary": "true"})
        api("/Library/VirtualFolders?" + query, {"LibraryOptions": {
            "Enabled": True,
            "PathInfos": [{"Path": "/media"}],
            "EnableRealtimeMonitor": False,
            "SaveLocalMetadata": False,
            "EnableChapterImageExtraction": False,
            "EnableTrickplayImageExtraction": False,
        }})
    print("Admin and non-admin viewer policies, explicit remote-access policy, hardware transcoding and read-only media library configured.")
finally:
    api("/Sessions/Logout", method="POST")
