import { act, screen, waitFor } from "@testing-library/react";
import { describe } from "vitest";
import { PeerConfig } from "../src/models/Peer";
import { AppRoutes } from "../src/routes";
import { writeTextToClipboard } from "../src/utils/browser";
import { getUserConfirmation, notifyUser } from "../src/utils/messaging";
import { getNetworkDetailsPageUrl } from "../src/utils/networks";
import { extractPeerPrivateEndpoints, extractPeerPublicEndpoint } from "../src/utils/peers";
import { MOCK_CHOICE, setupMocks } from "./tests";

describe("networks utility functions", () => {
  beforeEach(() => {
    setupMocks();
  });

  it("provides a function to form network details page URL from network id", () => {
    const mockNetworkId = "mock-net";

    const networkDetailsUrl = getNetworkDetailsPageUrl(mockNetworkId);

    expect(networkDetailsUrl).toEqual(
      AppRoutes.NETWORK_DETAILS_ROUTE.split(":")?.[0] + `${mockNetworkId}`
    );
  });
});

describe("messaging utility functions", () => {
  beforeEach(() => {
    setupMocks();
  });

  it("provides a function to notify users", async () => {
    expect(await getUserConfirmation("test message", "test title")).toEqual(
      false
    );
  });

  it("provides a function to get user's confirmations", async () => {
    expect(await notifyUser("test message")).toEqual(MOCK_CHOICE);
  });
});

describe("browser utility functions", () => {
  beforeEach(() => {
    setupMocks();
  });

  it("provides a function to write text to clipboard", async () => {
    const mockText = "test message";

    expect(await writeTextToClipboard(mockText)).toEqual(mockText);
  });
});

describe("peers utility functions", () => {
  beforeEach(() => {
    setupMocks();
  });

  it("provides a function to get peer public endpoint", () => {
    const mockPeer: PeerConfig = {
      PublicKey: [56, 65, 75, 77],
      Endpoint: { IP: "51.0.0.1", Port: 38378, Zone: "" },
      AllowedIPs: [{ IP: "10.0.0.51", Mask: "w+" }],
      Remove: false,
      UpdateOnly: false,
      PersistentKeepaliveInterval: 0,
      ReplaceAllowedIPs: false
    };

    const expected = `${mockPeer.Endpoint.IP}:${mockPeer.Endpoint.Port}`

    expect(extractPeerPublicEndpoint(mockPeer)).toEqual(expected);
    expect(extractPeerPrivateEndpoints(mockPeer)).toEqual(["10.0.0.51"]);
  });
});
