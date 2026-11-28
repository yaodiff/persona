package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Test

class CompactFolderNameTest {
    @Test fun extractsLastSegmentForPosixAndWindowsPaths() {
        assertEquals("sample-net", compactFolderName("C:\\Users\\demo\\Workspace\\sample-net"))
        assertEquals("sample-net", compactFolderName("C:\\Users\\demo\\Workspace\\sample-net\\"))
        assertEquals("sample-net", compactFolderName("C:/Users/demo/Workspace/sample-net/"))
        assertEquals("sample-net", compactFolderName("/home/demo/workspace/sample-net"))
        assertEquals("sample-net", compactFolderName("/home/demo/workspace/sample-net/"))
        assertEquals("mixed", compactFolderName("C:\\Users\\demo/workspace\\mixed\\"))
        assertEquals("share", compactFolderName("\\\\server\\share\\"))
    }

    @Test fun returnsEmptyForRootsAndBlanks() {
        assertEquals("", compactFolderName(null))
        assertEquals("", compactFolderName(""))
        assertEquals("", compactFolderName("   "))
        assertEquals("", compactFolderName("/"))
        assertEquals("", compactFolderName("C:\\"))
        assertEquals("", compactFolderName("C:/"))
        assertEquals("", compactFolderName("D:"))
    }

    @Test fun sessionFolderNameAndPlanTaskProjectHandleWindowsPaths() {
        val session = Session(
            machineId = "workstation",
            sessionId = "s1",
            cwd = "C:\\Users\\demo\\Workspace\\sample-net",
        )
        assertEquals("sample-net", session.folderName)

        val task = PlanTask(
            title = "Audit",
            cwd = "D:\\Projects\\persona-mobile\\",
        )
        assertEquals("persona-mobile", task.project)
    }
}
