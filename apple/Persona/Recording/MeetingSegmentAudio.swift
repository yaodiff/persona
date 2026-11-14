#if os(iOS)
import AVFoundation
import Foundation

/// A stretch of a recording's audio read back from the phone's own segments (`seg-NNN.m4a`, 16 kHz mono AAC) as PCM16
/// little-endian, for `PUT …/audio?at=` after the meeting (#3862 phase 5, docs/assistant/meeting-minutes.md「Gaps and the
/// final pass」). File reads and decoding: called off the main thread (`MeetingTranscription.Deps.live` runs it detached).
nonisolated enum MeetingSegmentAudio {
    /// The audio from `at` for at most `seconds`: from the first sample the segments hold in that stretch, as far as they
    /// hold it without a hole; nil when they hold none of it. `at` in the result is where the audio really starts.
    static func pcm(folder: URL, segments: [RecordingSegment], at: Double, seconds: Double) -> (at: Double, data: Data)? {
        let rate = Double(MeetingAudioClock.rate)
        let from = MeetingAudioClock.sample(at), to = MeetingAudioClock.sample(at + seconds)
        guard to > from else { return nil }
        var out = Data()
        var first: Int64?
        var next: Int64 = from
        for segment in segments.sorted(by: { $0.start < $1.start }) {
            let segStart = MeetingAudioClock.sample(segment.start), segEnd = MeetingAudioClock.sample(segment.start + segment.duration)
            guard segEnd > next, segStart < to else { continue }
            // A hole before this segment ends what can be sent in one piece (once something was read).
            if first != nil, segStart > next { break }
            let begin = max(next, segStart)
            guard let read = samples(folder.appending(path: RecordingSegment.fileName(segment.index)),
                                     offset: begin - segStart, count: min(to, segEnd) - begin, rate: rate), !read.isEmpty else {
                if first != nil { break } else { continue }
            }
            if first == nil { first = begin }
            out.append(MeetingPCM.data(read[...]))
            next = begin + Int64(read.count)
            if next >= to { break }
        }
        guard let first, !out.isEmpty else { return nil }
        return (MeetingAudioClock.seconds(first), out)
    }

    /// `count` samples of `url` from sample `offset`, as PCM16; nil when the file can't be read at 16 kHz mono.
    private static func samples(_ url: URL, offset: Int64, count: Int64, rate: Double) -> [Int16]? {
        guard count > 0, let file = try? AVAudioFile(forReading: url, commonFormat: .pcmFormatInt16, interleaved: false),
              file.processingFormat.sampleRate == rate, file.processingFormat.channelCount == 1 else { return nil }
        guard offset < file.length else { return [] }
        file.framePosition = max(0, offset)
        let want = AVAudioFrameCount(min(count, file.length - offset))
        guard want > 0, let buffer = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: want) else { return [] }
        do { try file.read(into: buffer, frameCount: want) } catch { return nil }
        guard let channel = buffer.int16ChannelData?[0] else { return nil }
        return Array(UnsafeBufferPointer(start: channel, count: Int(buffer.frameLength)))
    }
}
#endif
