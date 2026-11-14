import CoreLocation
import Foundation
import MapKit
import PersonaWire

/// Nearby search and handing a destination to Maps (#2281). Navigation happens in the Maps app: the tool says it opened
/// Maps, never that Persona navigates.
enum MapsTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_maps_search_nearby",
                    description: """
                    Search places near the user (or near the latitude / longitude given), such as "quiet cafe" or \
                    "pharmacy", with Apple Maps. Returns each place's name, address, latitude, longitude, distance in \
                    meters from the search center, category, phone and website, nearest first, and the source. \
                    Apple Maps does not give opening hours to apps: say so instead of guessing. Use \
                    phone_maps_navigate to hand one to Maps.
                    """,
                    inputSchema: Schema.object([
                        "query": Schema.string("What to look for, e.g. quiet cafe."),
                        "latitude": Schema.number("Search center latitude. Default: the phone's current location."),
                        "longitude": Schema.number("Search center longitude. Default: the phone's current location."),
                        "radius_meters": Schema.integer("How far around the center to look, default 1500, 100–50000.", min: 100, max: 50000),
                        "limit": Schema.integer("Maximum number of places, default 8, max 20.", min: 1, max: 20),
                    ], required: ["query"])
                ),
                source: .location,
                run: { args in try await searchNearby(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_maps_navigate",
                    description: """
                    Open directions to a place in the Apple Maps app, with a transport mode. Navigation then runs in \
                    Maps, not in Persona: tell the user Maps has opened, do not say you are navigating. Persona must \
                    be open on the iPhone.
                    """,
                    inputSchema: Schema.object([
                        "latitude": Schema.number("Destination latitude."),
                        "longitude": Schema.number("Destination longitude."),
                        "name": Schema.string("Destination name shown in Maps."),
                        "mode": Schema.string("driving (default), walking or transit.", values: ["driving", "walking", "transit"]),
                    ], required: ["latitude", "longitude"])
                ),
                source: .location,
                run: { args in try await navigate(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_maps_route",
                    description: """
                    Work out a route to a place with Apple Maps, without opening anything: from the phone's current \
                    location (or the from_latitude / from_longitude given) to the destination, by driving (default), \
                    walking or transit. Returns the distance in meters, the expected travel time in seconds and the \
                    route's line (a few points, which Persona draws on a map in the chat). For transit Apple Maps gives \
                    apps the time and distance only, no line. Use it for "how far" or "how long"; hand the route to Maps \
                    with phone_maps_navigate only when the user wants to go.
                    """,
                    inputSchema: Schema.object([
                        "latitude": Schema.number("Destination latitude."),
                        "longitude": Schema.number("Destination longitude."),
                        "name": Schema.string("Destination name."),
                        "mode": Schema.string("driving (default), walking or transit.", values: ["driving", "walking", "transit"]),
                        "from_latitude": Schema.number("Start latitude. Default: the phone's current location."),
                        "from_longitude": Schema.number("Start longitude. Default: the phone's current location."),
                    ], required: ["latitude", "longitude"])
                ),
                source: .location,
                run: { args in try await route(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// A destination handed to Maps.
    nonisolated struct Destination: Equatable, Sendable {
        nonisolated enum Mode: String, Sendable { case driving, walking, transit }

        var latitude: Double
        var longitude: Double
        var name: String?
        var mode: Mode

        static func parse(_ args: JSONValue) throws -> Destination {
            guard let latitude = args["latitude"]?.double, (-90...90).contains(latitude),
                  let longitude = args["longitude"]?.double, (-180...180).contains(longitude) else {
                throw ToolFailure(.invalidArgument, "latitude (-90…90) and longitude (-180…180) are required")
            }
            let raw = ToolArgs.string(args, "mode") ?? Mode.driving.rawValue
            guard let mode = Mode(rawValue: raw) else {
                throw ToolFailure(.invalidArgument, "mode must be driving, walking or transit")
            }
            return Destination(latitude: latitude, longitude: longitude, name: ToolArgs.string(args, "name"), mode: mode)
        }

        var directionsMode: String {
            switch mode {
            case .driving: MKLaunchOptionsDirectionsModeDriving
            case .walking: MKLaunchOptionsDirectionsModeWalking
            case .transit: MKLaunchOptionsDirectionsModeTransit
            }
        }

        var transportType: MKDirectionsTransportType {
            switch mode {
            case .driving: .automobile
            case .walking: .walking
            case .transit: .transit
            }
        }
    }

    /// Where a route starts (`phone_maps_route`): the coordinates given, or nil for the phone's current location. One of
    /// the two alone, or either out of range, is a mistake the model can fix.
    nonisolated static func routeStart(_ args: JSONValue) throws -> (latitude: Double, longitude: Double)? {
        switch (args["from_latitude"]?.double, args["from_longitude"]?.double) {
        case (nil, nil):
            return nil
        case let (latitude?, longitude?) where (-90...90).contains(latitude) && (-180...180).contains(longitude):
            return (latitude, longitude)
        default:
            throw ToolFailure(.invalidArgument, "from_latitude (-90…90) and from_longitude (-180…180) go together")
        }
    }

    #if os(iOS)
    static func searchNearby(_ args: JSONValue) async throws -> [ToolContent] {
        guard let query = ToolArgs.string(args, "query") else {
            throw ToolFailure(.invalidArgument, "query is required")
        }
        let radius = Double(ToolArgs.int(args, "radius_meters", fallback: 1500, min: 100, max: 50000))
        let limit = ToolArgs.int(args, "limit", fallback: 8, min: 1, max: 20)
        let center: CLLocation
        var centerSource = "given"
        if let latitude = args["latitude"]?.double, let longitude = args["longitude"]?.double {
            center = CLLocation(latitude: latitude, longitude: longitude)
        } else {
            switch LocationAccess.status() {
            case .granted, .limited, .notNeeded: break
            case .denied: throw ToolFailure(.denied, "Location")
            case .notDetermined: throw ToolFailure(.denied, "Location access has not been requested yet")
            }
            center = try await OneShotLocation.fetch(timeout: 15)
            centerSource = "current_location"
        }

        let request = MKLocalSearch.Request()
        request.naturalLanguageQuery = query
        request.region = MKCoordinateRegion(center: center.coordinate, latitudinalMeters: radius * 2, longitudinalMeters: radius * 2)
        request.resultTypes = .pointOfInterest
        let items: [MKMapItem]
        do {
            items = try await MKLocalSearch(request: request).start().mapItems
        } catch {
            throw ToolFailure(.unavailable, "Apple Maps found nothing for \"\(query)\": \(error.localizedDescription)")
        }
        let rows = items.map { item -> (Double, JSONValue) in
            let place = item.placemark
            let distance = center.distance(from: CLLocation(latitude: place.coordinate.latitude, longitude: place.coordinate.longitude))
            return (distance, Output.object([
                ("name", Output.opt(item.name)),
                ("address", Output.opt(place.title)),
                ("latitude", .number(place.coordinate.latitude)),
                ("longitude", .number(place.coordinate.longitude)),
                ("distance_meters", .number(distance.rounded())),
                ("category", Output.opt(item.pointOfInterestCategory?.rawValue)),
                ("phone", Output.opt(item.phoneNumber)),
                ("url", Output.opt(item.url?.absoluteString)),
            ]))
        }
        let nearest = rows.filter { $0.0 <= radius * 1.5 }.sorted { $0.0 < $1.0 }.prefix(limit).map(\.1)
        return Output.json(Output.object([
            ("query", .string(query)),
            ("center", Output.object([("latitude", .number(center.coordinate.latitude)),
                                      ("longitude", .number(center.coordinate.longitude)),
                                      ("from", .string(centerSource))])),
            ("radius_meters", .number(radius)),
            ("count", .number(Double(nearest.count))),
            ("places", .array(Array(nearest))),
            ("source", .string("Apple Maps (MapKit local search)")),
            ("note", .string("Opening hours are not available from Apple Maps to apps.")),
        ]))
    }

    static func navigate(_ args: JSONValue) async throws -> [ToolContent] {
        let destination = try Destination.parse(args)
        try Foreground.require("open Maps")
        let coordinate = CLLocationCoordinate2D(latitude: destination.latitude, longitude: destination.longitude)
        let item = MKMapItem(placemark: MKPlacemark(coordinate: coordinate))
        item.name = destination.name
        let opened = item.openInMaps(launchOptions: [MKLaunchOptionsDirectionsModeKey: destination.directionsMode])
        guard opened else {
            throw ToolFailure(.unavailable, "iOS did not open Maps. Ask the user to open Persona and try again.")
        }
        return Output.json(Output.object([
            ("opened", .string("maps")),
            ("destination", Output.opt(destination.name)),
            ("latitude", .number(destination.latitude)),
            ("longitude", .number(destination.longitude)),
            ("mode", .string(destination.mode.rawValue)),
            ("note", .string("Directions are open in the Maps app; navigation runs there, not in Persona.")),
        ]))
    }

    static func route(_ args: JSONValue) async throws -> [ToolContent] {
        let destination = try Destination.parse(args)
        let start = try routeStart(args)
        let request = MKDirections.Request()
        if let start {
            request.source = MKMapItem(placemark: MKPlacemark(coordinate: CLLocationCoordinate2D(latitude: start.latitude, longitude: start.longitude)))
        } else {
            switch LocationAccess.status() {
            case .granted, .limited, .notNeeded: break
            case .denied: throw ToolFailure(.denied, "Location")
            case .notDetermined: throw ToolFailure(.denied, "Location access has not been requested yet")
            }
            // Maps' own idea of where the phone is, not a Core Location fix: in mainland China only the former is in
            // the map's coordinates.
            request.source = MKMapItem.forCurrentLocation()
        }
        let target = MKMapItem(placemark: MKPlacemark(coordinate: CLLocationCoordinate2D(latitude: destination.latitude, longitude: destination.longitude)))
        target.name = destination.name
        request.destination = target
        request.transportType = destination.transportType
        request.requestsAlternateRoutes = false
        let directions = MKDirections(request: request)

        var pairs: [(String, JSONValue?)] = [
            ("destination", Output.opt(destination.name)),
            ("latitude", .number(destination.latitude)),
            ("longitude", .number(destination.longitude)),
            ("mode", .string(destination.mode.rawValue)),
        ]
        do {
            if destination.mode == .transit {
                // Apple Maps gives apps a transit time and distance, not the line.
                let eta = try await directions.calculateETA()
                pairs += [("distance_meters", .number(eta.distance.rounded())),
                          ("expected_seconds", .number(eta.expectedTravelTime.rounded()))]
            } else {
                guard let route = try await directions.calculate().routes.first else { throw MKError(.directionsNotFound) }
                let line = RouteLine.simplified(route.polyline.coordinateList).map { point -> JSONValue in
                    .array([.number(RouteLine.rounded(point.latitude)), .number(RouteLine.rounded(point.longitude))])
                }
                pairs += [("distance_meters", .number(route.distance.rounded())),
                          ("expected_seconds", .number(route.expectedTravelTime.rounded())),
                          ("route_name", Output.opt(route.name.isEmpty ? nil : route.name)),
                          ("line", .array(line))]
            }
        } catch {
            throw ToolFailure(.unavailable, "Apple Maps found no \(destination.mode.rawValue) route there: \(error.localizedDescription)")
        }
        pairs += [
            ("from", .string(start == nil ? "current_location" : "given")),
            ("source", .string("Apple Maps (MapKit directions)")),
            ("note", .string("Nothing was opened: Persona draws the route in the chat. Hand it to Maps with phone_maps_navigate when the user wants to go.")),
        ]
        return Output.json(Output.object(pairs))
    }
    #endif
}

/// A route's line made short enough for the model to read and the chat to draw (`phone_maps_route`, #5544).
nonisolated enum RouteLine {
    /// The most points a line keeps: plenty for a map the size of a chat card, about 1 KB of JSON.
    static let most = 48

    /// `points` with the bends that matter: Douglas–Peucker, its tolerance doubling from about a meter until at most
    /// `most` points are left. The ends always stay.
    static func simplified(_ points: [CLLocationCoordinate2D], most: Int = most) -> [CLLocationCoordinate2D] {
        guard points.count > most, most >= 2 else { return points }
        var tolerance = 0.00001
        while true {
            let kept = keep(points, tolerance: tolerance)
            if kept.count <= most { return kept }
            tolerance *= 2
        }
    }

    /// Five decimals: about a meter, as much as a drawn line needs.
    static func rounded(_ degrees: Double) -> Double { (degrees * 100_000).rounded() / 100_000 }

    private static func keep(_ p: [CLLocationCoordinate2D], tolerance: Double) -> [CLLocationCoordinate2D] {
        var kept = [Bool](repeating: false, count: p.count)
        kept[0] = true
        kept[p.count - 1] = true
        // A degree of longitude is shorter away from the equator.
        let scale = cos(p[0].latitude * .pi / 180)
        var spans = [(0, p.count - 1)]
        while let span = spans.popLast() {
            let (a, b) = span
            guard b > a + 1 else { continue }
            var far = a
            var farthest = 0.0
            for i in (a + 1)..<b {
                let d = offset(p[i], from: p[a], to: p[b], scale: scale)
                if d > farthest { (far, farthest) = (i, d) }
            }
            if farthest > tolerance {
                kept[far] = true
                spans.append((a, far))
                spans.append((far, b))
            }
        }
        return p.indices.filter { kept[$0] }.map { p[$0] }
    }

    /// How far `q` stands off the segment from `a` to `b`, in degrees of latitude.
    private static func offset(_ q: CLLocationCoordinate2D, from a: CLLocationCoordinate2D, to b: CLLocationCoordinate2D,
                               scale: Double) -> Double {
        let (ax, ay, bx, by) = (a.longitude * scale, a.latitude, b.longitude * scale, b.latitude)
        let (qx, qy) = (q.longitude * scale, q.latitude)
        let (dx, dy) = (bx - ax, by - ay)
        let length = dx * dx + dy * dy
        guard length > 0 else { return hypot(qx - ax, qy - ay) }
        let t = max(0, min(1, ((qx - ax) * dx + (qy - ay) * dy) / length))
        return hypot(qx - (ax + t * dx), qy - (ay + t * dy))
    }
}

private extension MKPolyline {
    /// The line's points as coordinates.
    var coordinateList: [CLLocationCoordinate2D] {
        var out = [CLLocationCoordinate2D](repeating: kCLLocationCoordinate2DInvalid, count: pointCount)
        getCoordinates(&out, range: NSRange(location: 0, length: pointCount))
        return out
    }
}
